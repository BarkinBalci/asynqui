package authz

// FoldUsername lowercases ASCII A–Z and leaves every other byte alone, so
// usernames compare case-insensitively without Unicode folding. Unicode
// folding would let look-alikes such as the Kelvin sign (U+212A) or the
// Angstrom sign (U+212B) match "k" or "å", and İ (U+0130) match "i".
func FoldUsername(s string) string {
	for i := 0; i < len(s); i++ {
		if c := s[i]; 'A' <= c && c <= 'Z' {
			b := []byte(s)
			for j := i; j < len(b); j++ {
				if c := b[j]; 'A' <= c && c <= 'Z' {
					b[j] = c + ('a' - 'A')
				}
			}
			return string(b)
		}
	}
	return s
}
