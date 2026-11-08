package config

import "testing"

// env returns a lookupEnv backed by m.
func env(m map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) {
		v, ok := m[k]
		return v, ok
	}
}

func TestExpand(t *testing.T) {
	vars := env(map[string]string{
		"HOST":  "redis",
		"PORT":  "6379",
		"EMPTY": "",
		"REF":   "${HOST}", // values from the environment are never re-expanded
		"DOLLA": "a$$b",
	})
	tests := []struct {
		name    string
		in      string
		want    string
		wantErr string
	}{
		{name: "plain text", in: "localhost:6379", want: "localhost:6379"},
		{name: "whole value", in: "${HOST}", want: "redis"},
		{name: "embedded", in: "${HOST}:${PORT}", want: "redis:6379"},
		{name: "set but empty is kept", in: "x${EMPTY}y", want: "xy"},
		{name: "default when unset", in: "${NOPE:-fallback}", want: "fallback"},
		{name: "set but empty ignores the default", in: "${EMPTY:-fallback}", want: ""},
		{name: "default ignored when set", in: "${HOST:-fallback}", want: "redis"},
		{name: "empty default", in: "${NOPE:-}", want: ""},
		{name: "default with colon and dash", in: "${NOPE:-a:-b}", want: "a:-b"},
		{name: "nested default", in: "${NOPE:-${HOST}}", want: "redis"},
		{name: "doubly nested default", in: "${NOPE:-${ALSO_NOPE:-deep}}", want: "deep"},
		{name: "nested default with text", in: "${NOPE:-x${PORT}y}", want: "x6379y"},
		{name: "braces in default", in: "${NOPE:-{a}}", want: "{a}"},
		{name: "env value not re-expanded", in: "${REF}", want: "${HOST}"},
		{name: "env value with $$ kept", in: "${DOLLA}", want: "a$$b"},
		{name: "$$ is a literal $", in: "pa$$word", want: "pa$word"},
		{name: "$${ escapes a reference", in: "$${HOST}", want: "${HOST}"},
		{name: "$$$ then reference", in: "$$${HOST}", want: "$redis"},
		{name: "lone $ kept", in: "cost: 5$", want: "cost: 5$"},
		{name: "bcrypt hash kept", in: "$2b$12$abc./XYZ", want: "$2b$12$abc./XYZ"},
		{name: "$ before non-brace kept", in: "$HOST", want: "$HOST"},
		{name: "unset", in: "${NOPE}", wantErr: "environment variable NOPE is not set"},
		{name: "unset inside default", in: "${NOPE:-${ALSO_NOPE}}", wantErr: "environment variable ALSO_NOPE is not set"},
		{name: "unterminated", in: "${HOST", wantErr: "unterminated ${ in value"},
		{name: "unterminated nested", in: "${NOPE:-${HOST}", wantErr: "unterminated ${ in value"},
		{name: "empty name", in: "${}", wantErr: `invalid variable name "" in ${...}`},
		{name: "name starts with digit", in: "${1A}", wantErr: `invalid variable name "1A" in ${...}`},
		{name: "shell-style default without colon", in: "${A-b}", wantErr: `invalid variable name "A-b" in ${...}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := expand(tt.in, vars)
			if tt.wantErr != "" {
				if err == nil || err.Error() != tt.wantErr {
					t.Fatalf("expand(%q) error = %v, want %q", tt.in, err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("expand(%q): %v", tt.in, err)
			}
			if got != tt.want {
				t.Errorf("expand(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestValidVarName(t *testing.T) {
	tests := []struct {
		name string
		want bool
	}{
		{"A", true}, {"_", true}, {"a_B9", true}, {"MAIN_REDIS_PASSWORD", true},
		{"", false}, {"9A", false}, {"A-B", false}, {"A B", false}, {"Ä", false},
	}
	for _, tt := range tests {
		if got := validVarName(tt.name); got != tt.want {
			t.Errorf("validVarName(%q) = %v, want %v", tt.name, got, tt.want)
		}
	}
}
