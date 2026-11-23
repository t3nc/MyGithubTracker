// Package web 内嵌前端静态资源。
package web

import "embed"

// FS 内嵌 index.html / style.css / app.js。
//
//go:embed index.html style.css app.js favicon.ico
var FS embed.FS
