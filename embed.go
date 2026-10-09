// Package passpal 暴露内嵌的前端构建产物。
//
// 根包不包含 main 函数：可执行入口在 cmd/passpal。
// 这样 web/dist 既能被 go:embed 打进二进制，也能被 cmd 包 import。
package passpal

import (
	"embed"
	"strconv"
	"strings"
)

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

// MigrationsDir 是 Migrations 中迁移文件所在的目录名。
const MigrationsDir = "migrations"

// MaxMigrationVersion 返回内嵌迁移中的最高版本号。
//
// restore 用它拒绝「比当前二进制更新的备份」。这个值以前是 main.go 里的
// 手写常量，加了 0003 / 0004 之后忘了同步，导致恢复功能静默失效 ——
// 改为从迁移文件推导，从根上杜绝再次漂移。
func MaxMigrationVersion() int {
	entries, err := Migrations.ReadDir(MigrationsDir)
	if err != nil {
		return 0
	}
	max := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		// 文件名形如 0003_add_refresh_token.sql，取第一段作为版本号。
		head, _, _ := strings.Cut(e.Name(), "_")
		n, err := strconv.Atoi(head)
		if err != nil {
			continue
		}
		if n > max {
			max = n
		}
	}
	return max
}
