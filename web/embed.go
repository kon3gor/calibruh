// Package web holds the HTML templates and static assets, Tailwind included,
// so the binary needs nothing from the network.
package web

import "embed"

//go:embed templates static
var FS embed.FS
