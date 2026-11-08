package config

import (
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

// resolver walks the parsed YAML tree alongside the Config type. It:
//
//   - interpolates environment variables into scalar values (never keys),
//   - reports unknown fields, null list entries and values of the wrong
//     kind or type, by line and location only, so no value (literal or from
//     the environment) ever appears in an error,
//   - records the line of every location for validation errors.
//
// After a clean walk, decoding the tree cannot fail on a value.
type resolver struct {
	lookupEnv func(string) (string, bool)
	lines     map[string]int
	seen      map[visit]bool
	// expanded maps each interpolated node to the type it was resolved for.
	expanded map[*yaml.Node]reflect.Type
	errs     []*fieldError
}

type visit struct {
	n *yaml.Node
	t reflect.Type
}

func newResolver(lookupEnv func(string) (string, bool)) *resolver {
	return &resolver{
		lookupEnv: lookupEnv,
		lines:     map[string]int{},
		seen:      map[visit]bool{},
		expanded:  map[*yaml.Node]reflect.Type{},
	}
}

// errorf records a problem. Pass the line of the key or node at fault; for
// values reached through an alias, scalarf uses the alias's line instead.
func (r *resolver) errorf(line int, path, format string, args ...any) {
	r.errs = append(r.errs, &fieldError{path: path, line: line, msg: fmt.Sprintf(format, args...)})
}

// walk resolves node n, which decodes into a value of type t at path.
func (r *resolver) walk(n *yaml.Node, t reflect.Type, path string) {
	if _, ok := r.lines[path]; !ok && path != "" {
		r.lines[path] = n.Line
	}
	// Aliases can make a node reachable many times; resolve each node once
	// per type to keep the walk linear.
	if r.seen[visit{n, t}] {
		return
	}
	r.seen[visit{n, t}] = true

	switch n.Kind {
	case yaml.DocumentNode:
		for _, c := range n.Content {
			r.walk(c, t, path)
		}
	case yaml.AliasNode:
		r.walk(n.Alias, t, path)
	case yaml.MappingNode:
		r.mapping(n, t, path)
	case yaml.SequenceNode:
		r.sequence(n, t, path)
	case yaml.ScalarNode:
		r.scalar(n, t, path)
	}
}

func (r *resolver) mapping(n *yaml.Node, t reflect.Type, path string) {
	var fields map[string]reflect.Type
	switch t.Kind() {
	case reflect.Struct:
		fields = yamlFields(t)
	case reflect.Map:
	default:
		r.errorf(n.Line, path, "must be %s, not a mapping", describe(t))
		return
	}
	// Explicit keys first: they override merged ones, so their lines win.
	var merges []*yaml.Node
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, v := n.Content[i], n.Content[i+1]
		if k.ShortTag() == "!!merge" {
			merges = append(merges, v)
			continue
		}
		child := joinPath(path, k.Value)
		if fields == nil {
			r.walk(v, t.Elem(), child)
			continue
		}
		ft, ok := fields[k.Value]
		if !ok {
			r.errorf(k.Line, child, "unknown field")
			continue
		}
		r.walk(v, ft, child)
	}
	for _, v := range merges {
		r.merge(v, t, path)
	}
}

// merge resolves the value of a "<<" merge key: a mapping, an alias to one,
// or a sequence of those.
func (r *resolver) merge(v *yaml.Node, t reflect.Type, path string) {
	if v.Kind == yaml.SequenceNode {
		for _, c := range v.Content {
			r.walk(c, t, path)
		}
		return
	}
	r.walk(v, t, path)
}

func (r *resolver) sequence(n *yaml.Node, t reflect.Type, path string) {
	if t.Kind() != reflect.Slice {
		r.errorf(n.Line, path, "must be %s, not a list", describe(t))
		return
	}
	for i, c := range n.Content {
		p := fmt.Sprintf("%s[%d]", path, i)
		// The decoder silently drops null entries; make them visible.
		if c.Kind == yaml.ScalarNode && c.ShortTag() == "!!null" && !r.isExpanded(c) {
			r.lines[p] = c.Line
			r.errorf(c.Line, p, "must not be empty")
			continue
		}
		r.walk(c, t.Elem(), p)
	}
}

func (r *resolver) isExpanded(n *yaml.Node) bool {
	_, ok := r.expanded[n]
	return ok
}

// scalarf records a problem with the scalar at path, at the line where the
// path's value is written (an alias, not its anchor).
func (r *resolver) scalarf(path, format string, args ...any) {
	r.errorf(r.lines[path], path, format, args...)
}

func (r *resolver) scalar(n *yaml.Node, t reflect.Type, path string) {
	if prev, ok := r.expanded[n]; ok {
		// An alias of an interpolated value used for another kind of field.
		if typeClass(prev) != typeClass(t) {
			r.scalarf(path, "an interpolated value aliased here must be %s", describe(prev))
		}
		return
	}
	if strings.Contains(n.Value, "$") {
		v, err := expand(n.Value, r.lookupEnv)
		if err != nil {
			r.expanded[n] = t
			r.scalarf(path, "%v", err)
			return
		}
		if v != n.Value {
			r.expanded[n] = t
			n.Value = v
			if !retype(n, t) {
				r.scalarf(path, "must be %s", describe(t))
				return
			}
		}
	}
	if !decodes(n, t) {
		r.scalarf(path, "must be %s", describe(t))
	}
}

// retype makes an interpolated scalar read as its field's type, whatever its
// quoting: verbatim for strings, decimal for integers, re-resolved (as if
// written unquoted) for everything else. An empty value of a non-string
// field reads as null, which keeps the default. It reports false for an
// integer that is not decimal.
func retype(n *yaml.Node, t reflect.Type) bool {
	if t.Kind() == reflect.String {
		n.Tag = "!!str"
		return true
	}
	n.Tag, n.Style = "", 0
	if n.Value == "" || !isInt(t) {
		return true
	}
	i, err := strconv.ParseInt(n.Value, 10, 64)
	if err != nil {
		return false
	}
	n.Value = strconv.FormatInt(i, 10) // 010 is ten, not octal eight
	return true
}

// decodes reports whether scalar n decodes into a value of type t. Errors are
// dropped because they quote the value.
func decodes(n *yaml.Node, t reflect.Type) bool {
	switch t.Kind() {
	case reflect.Slice, reflect.Struct, reflect.Map:
		return n.ShortTag() == "!!null"
	}
	return n.Decode(reflect.New(t).Interface()) == nil
}

func isDuration(t reflect.Type) bool { return t == reflect.TypeFor[time.Duration]() }

func isInt(t reflect.Type) bool {
	return !isDuration(t) && t.Kind() >= reflect.Int && t.Kind() <= reflect.Int64
}

// typeClass groups types that read a scalar the same way.
func typeClass(t reflect.Type) string {
	if isDuration(t) {
		return "duration"
	}
	return t.Kind().String()
}

// describe names what a field of type t expects, for error messages.
func describe(t reflect.Type) string {
	switch {
	case isDuration(t):
		return "a duration such as 5s"
	case isInt(t):
		return "a decimal integer"
	}
	switch t.Kind() {
	case reflect.Bool:
		return "true or false"
	case reflect.String:
		return "a string"
	case reflect.Slice:
		return "a list"
	default:
		return "a mapping"
	}
}

// yamlFields maps each YAML key of struct type t to its field type. Inline
// and embedded fields are not supported (TestNoInlineFields guards this).
func yamlFields(t reflect.Type) map[string]reflect.Type {
	fields := make(map[string]reflect.Type, t.NumField())
	for i := range t.NumField() {
		f := t.Field(i)
		name, _, _ := strings.Cut(f.Tag.Get("yaml"), ",")
		if !f.IsExported() || name == "-" {
			continue
		}
		if name == "" {
			name = strings.ToLower(f.Name)
		}
		fields[name] = f.Type
	}
	return fields
}

func joinPath(parent, key string) string {
	if parent == "" {
		return key
	}
	return parent + "." + key
}

// lineFor returns the line of path, or of its closest ancestor present in the
// file, or 0.
func (r *resolver) lineFor(path string) int {
	for path != "" {
		if l, ok := r.lines[path]; ok {
			return l
		}
		cut := strings.LastIndexAny(path, ".[")
		if cut < 0 {
			return 0
		}
		path = path[:cut]
	}
	return 0
}
