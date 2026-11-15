// Package migrations embeds the exact SQL also provided for manual initialization.
package migrations

import _ "embed"

//go:embed init.sql
var Init string
