// Package web holds the templ views and the static assets (CSS, vendored JS)
// for the Financas UI. Static assets are embedded into the binary so the
// production image is a single self-contained artifact (AD-8).
package web

import "embed"

// StaticFS is the embedded static-asset tree, served under /static. The
// `all:` prefix embeds dotfiles too (e.g. .gitkeep), keeping the directory
// present even before the first Tailwind build.
//
//go:embed all:static
var StaticFS embed.FS
