//go:build arl_noui

package web

import "io/fs"

// Embedded reports whether the binary carries the UI.
const Embedded = false

// Dist returns nil: this binary serves the API only.
func Dist() fs.FS { return nil }
