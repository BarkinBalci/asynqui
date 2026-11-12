package format

import "testing"

func TestPrettyJSON(t *testing.T) {
	tests := []struct {
		in   string
		want string
		ok   bool
	}{
		{`{"a":1,"b":[1,2]}`, "{\n  \"a\": 1,\n  \"b\": [\n    1,\n    2\n  ]\n}", true},
		{`{"n":12345678901234567890}`, "{\n  \"n\": 12345678901234567890\n}", true},
		{`"x"`, `"x"`, true},
		{`{"a":`, "", false},
		{"", "", false},
		{"  ", "", false},
		{"\x00\x01", "", false},
		{`<script>alert(1)</script>`, "", false},
	}
	for _, tt := range tests {
		got, ok := PrettyJSON([]byte(tt.in))
		if got != tt.want || ok != tt.ok {
			t.Errorf("PrettyJSON(%q) = %q, %v; want %q, %v", tt.in, got, ok, tt.want, tt.ok)
		}
	}
}

func TestText(t *testing.T) {
	if got := Text([]byte("ok\xffé")); got != "ok�é" {
		t.Errorf("Text = %q", got)
	}
}

func TestPreview(t *testing.T) {
	tests := []struct {
		in   string
		n    int
		want string
	}{
		{"short", 10, "short"},
		{"exactly10!", 10, "exactly10!"},
		{"eleven chars", 11, "eleven char…"},
		{"a\n  b\tc", 10, "a b c"},
		{"  lead", 10, "lead"},
		{"trail  ", 10, "trail"},
		{"ab cd", 2, "ab…"},
		{"ab cd", 3, "ab …"},
		{"çğüşöı", 3, "çğü…"},
		{"", 5, ""},
	}
	for _, tt := range tests {
		if got := Preview(tt.in, tt.n); got != tt.want {
			t.Errorf("Preview(%q, %d) = %q, want %q", tt.in, tt.n, got, tt.want)
		}
	}
}
