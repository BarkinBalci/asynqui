package format

import "strconv"

// Int formats an identifier-like integer without group separators: a PID
// ("41", "40213") or a DB number. Counts use Number.
func Int(n int64) string {
	return strconv.FormatInt(n, 10)
}
