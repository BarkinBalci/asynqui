package config

import (
	"errors"
	"fmt"
	"strings"
)

// expand interpolates environment variables into one scalar value:
//
//   - ${NAME} is replaced by the variable's value. An unset variable is an
//     error; a variable set to "" expands to "".
//   - ${NAME:-default} uses default when NAME is unset; a variable set to ""
//     still expands to "". The default is itself expanded, so ${A:-${B}}
//     works; braces inside it must be balanced.
//   - $$ produces a literal $, so $${NAME} is the literal text ${NAME}.
//   - Any other $ is kept as is, so bcrypt hashes need no escaping.
//
// Values read from the environment are never expanded again.
func expand(s string, lookupEnv func(string) (string, bool)) (string, error) {
	var b strings.Builder
	for i := 0; i < len(s); {
		switch {
		case strings.HasPrefix(s[i:], "$$"):
			b.WriteByte('$')
			i += 2
		case strings.HasPrefix(s[i:], "${"):
			end := closingBrace(s, i+2)
			if end < 0 {
				return "", errors.New("unterminated ${ in value")
			}
			v, err := lookup(s[i+2:end], lookupEnv)
			if err != nil {
				return "", err
			}
			b.WriteString(v)
			i = end + 1
		default:
			b.WriteByte(s[i])
			i++
		}
	}
	return b.String(), nil
}

// closingBrace returns the index of the "}" that closes a "${" whose body
// starts at from, or -1. Nested braces in a default are balanced.
func closingBrace(s string, from int) int {
	depth := 1
	for i := from; i < len(s); i++ {
		switch s[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

// lookup resolves the body of one ${...} reference.
func lookup(expr string, lookupEnv func(string) (string, bool)) (string, error) {
	name, def, hasDefault := strings.Cut(expr, ":-")
	if !validVarName(name) {
		return "", fmt.Errorf("invalid variable name %q in ${...}", name)
	}
	v, ok := lookupEnv(name)
	switch {
	case ok:
		return v, nil
	case hasDefault:
		return expand(def, lookupEnv)
	}
	return "", fmt.Errorf("environment variable %s is not set", name)
}

// validVarName reports whether name matches [A-Za-z_][A-Za-z0-9_]*.
func validVarName(name string) bool {
	if name == "" {
		return false
	}
	for i, c := range name {
		letter := c == '_' || ('a' <= c && c <= 'z') || ('A' <= c && c <= 'Z')
		if !letter && (i == 0 || c < '0' || c > '9') {
			return false
		}
	}
	return true
}
