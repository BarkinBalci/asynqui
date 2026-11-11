package format

import "testing"

func TestInt(t *testing.T) {
	for n, want := range map[int64]string{0: "0", 41: "41", 40213: "40213", -1: "-1"} {
		if got := Int(n); got != want {
			t.Errorf("Int(%d) = %q, want %q", n, got, want)
		}
	}
}
