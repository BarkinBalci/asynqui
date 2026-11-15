package config

import "testing"

func TestExpand(t *testing.T) {
	vars := env(map[string]string{"HOST": "redis", "PORT": "6379", "EMPTY": "", "REF": "${HOST}"})
	tests := []struct {
		in, want, wantErr string
	}{
		{in: "localhost:6379", want: "localhost:6379"},
		{in: "${HOST}:${PORT}", want: "redis:6379"},
		{in: "x${EMPTY}y", want: "xy"},
		{in: "${NOPE:-a:-b}", want: "a:-b"},
		{in: "${EMPTY:-fallback}", want: ""},
		{in: "${REF}", want: "${HOST}"}, // values are not expanded again
		{in: "$${HOST} pa$$word", want: "${HOST} pa$word"},
		{in: "$2b$12$abc./XYZ $HOST 5$", want: "$2b$12$abc./XYZ $HOST 5$"},
		{in: "${NOPE}", wantErr: "environment variable NOPE is not set"},
		{in: "${HOST", wantErr: "unterminated ${"},
		{in: "${NOPE:-${HOST}}", wantErr: "nested ${ is not supported"},
		{in: "${1A}", wantErr: `invalid variable name "1A" in ${...}`},
	}
	for _, tt := range tests {
		got, err := expand(tt.in, vars)
		switch {
		case tt.wantErr != "" && (err == nil || err.Error() != tt.wantErr):
			t.Errorf("expand(%q) error = %v, want %q", tt.in, err, tt.wantErr)
		case tt.wantErr == "" && (err != nil || got != tt.want):
			t.Errorf("expand(%q) = %q, %v, want %q", tt.in, got, err, tt.want)
		}
	}
}
