// Package web is the phone app and the guest page, embedded into the binary.
package web

import "embed"

// Files are the pages, scripts, styles and icons the server hands out.
//
//go:embed *.html *.js *.css *.png *.webmanifest
var Files embed.FS
