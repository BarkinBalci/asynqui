package config

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"go.yaml.in/yaml/v3"
)

// interpolate expands environment variables in the scalar values (never the
// keys) of the tree at n. An expanded value is read as if written unquoted, so
// ${DB} fills an int; an empty one is null and keeps the field's default.
// Aliases are skipped, since their anchors are expanded in place. Null list
// entries are rejected, since decoding would silently drop them.
func interpolate(n *yaml.Node, lookupEnv func(string) (string, bool)) error {
	switch n.Kind {
	case yaml.ScalarNode:
		if !strings.Contains(n.Value, "$") {
			return nil
		}
		v, err := expand(n.Value, lookupEnv)
		if err != nil {
			return fmt.Errorf("line %d: %w", n.Line, err)
		}
		if v != n.Value {
			n.Value, n.Tag, n.Style = v, "", 0
			if v != "" && n.ShortTag() == "!!null" {
				n.Tag = "!!str" // "null" from the environment is a string
			}
		}
	case yaml.MappingNode:
		for i := 1; i < len(n.Content); i += 2 {
			if err := interpolate(n.Content[i], lookupEnv); err != nil {
				return err
			}
		}
	case yaml.SequenceNode:
		for _, c := range n.Content {
			if err := interpolate(c, lookupEnv); err != nil {
				return err
			}
			if c.Kind == yaml.ScalarNode && c.ShortTag() == "!!null" {
				return fmt.Errorf("line %d: list entry must not be empty", c.Line)
			}
		}
	}
	return nil
}

// expand expands ${NAME}, ${NAME:-default} and $$ in s (see
// docs/configuration.md#interpolation).
func expand(s string, lookupEnv func(string) (string, bool)) (string, error) {
	validName := regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	var b strings.Builder
	for {
		i := strings.IndexByte(s, '$')
		if i < 0 || i == len(s)-1 {
			b.WriteString(s)
			return b.String(), nil
		}
		b.WriteString(s[:i])
		switch s[i+1] {
		case '$':
			b.WriteByte('$')
			s = s[i+2:]
		case '{':
			body, rest, ok := strings.Cut(s[i+2:], "}")
			switch {
			case !ok:
				return "", errors.New("unterminated ${")
			case strings.Contains(body, "${"):
				return "", errors.New("nested ${ is not supported")
			}
			name, def, hasDefault := strings.Cut(body, ":-")
			if !validName.MatchString(name) {
				return "", fmt.Errorf("invalid variable name %q in ${...}", name)
			}
			v, set := lookupEnv(name)
			switch {
			case set:
			case hasDefault:
				v = def
			default:
				return "", fmt.Errorf("environment variable %s is not set", name)
			}
			b.WriteString(v)
			s = rest
		default:
			b.WriteByte('$')
			s = s[i+1:]
		}
	}
}
