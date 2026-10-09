// Package passpal 暴露内嵌的前端构建产物。
//
// 根包不包含 main 函数：可执行入口在 cmd/passpal。
// 这样 web/dist 既能被 go:embed 打进二进制，也能被 cmd 包 import。
package passpal

import "embed"

// WebDist 是 Vite 构建产物（web/dist）。
// 构建前端后无需额外步骤，go build 会自动包含最新产物。
//
//go:embed all:web/dist
var WebDist embed.FS

// WebDistRoot 是 WebDist 中静态资源的根目录。
const WebDistRoot = "web/dist"

// Migrations 内嵌 SQL 迁移文件，保证二进制自带 schema 演进。
//
//go:embed all:migrations
var Migrations embed.FS
