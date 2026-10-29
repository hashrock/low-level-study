// Package examples embeds the example scripts and their data files so the
// browser build can offer them as presets and preload them into its
// in-memory filesystem (at the same relative paths as in this repo).
package examples

import "embed"

//go:embed *.js data/input.txt data/lib.js
var FS embed.FS
