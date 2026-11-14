package authz

import "strings"

// FoldUsername lowercases ASCII A–Z and leaves every other rune alone, so
// usernames compare case-insensitively without Unicode folding. Unicode
// folding would let look-alikes such as the Kelvin sign (U+212A) or the
// Angstrom sign (U+212B) match "k" or "å", and İ (U+0130) match "i".
func FoldUsername(s string) string {
	return strings.Map(func(r rune) rune {
		if 'A' <= r && r <= 'Z' {
			return r + ('a' - 'A')
		}
		return r
	}, s)
}
