// Package web embeds the browser UI.
package web

import "embed"

//go:embed index.html *.js *.css vendor
var Files embed.FS
