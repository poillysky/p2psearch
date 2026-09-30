package web

import "embed"

//go:embed index.html settings.html app.js settings.js style.css
var FS embed.FS
