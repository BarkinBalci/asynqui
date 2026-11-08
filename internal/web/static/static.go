// Package static embeds the CSS, JavaScript, fonts and images served under
// /static/.
package static

import "embed"

// FS holds the embedded static assets.
//
//go:embed files
var FS embed.FS
