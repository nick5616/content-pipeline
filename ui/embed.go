// Package ui embeds the built web UI. `task ui:build` fills dist/; until then
// it holds only .gitkeep and the server shows a "UI not built" page.
package ui

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var dist embed.FS

// Dist is the built UI, rooted at dist/.
var Dist, _ = fs.Sub(dist, "dist")
