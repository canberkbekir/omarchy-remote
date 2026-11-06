// Package themes holds the built-in themes, embedded into the binary.
package themes

import "embed"

// Files are the theme stylesheets, one <name>.css each.
//
//go:embed *.css
var Files embed.FS
