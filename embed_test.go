package passpal

import "testing"

// TestMaxMigrationVersion 防止「加了新迁移但 restore 的上限没跟着走」。
//
// 这个值曾经是 main.go 里的手写常量，加了 0003 / 0004 后忘记同步，
// 导致恢复功能静默失效（恢复时报「候选库迁移版本 4 高于当前支持的 2」）。
func TestMaxMigrationVersion(t *testing.T) {
	got := MaxMigrationVersion()
	if got <= 0 {
		t.Fatalf("未能从内嵌迁移推导出版本号：%d", got)
	}

	// 必须等于目录里实际存在的最高版本，而不是某个写死的数。
	entries, err := Migrations.ReadDir(MigrationsDir)
	if err != nil {
		t.Fatalf("读取内嵌迁移目录失败：%v", err)
	}
	files := 0
	for _, e := range entries {
		if !e.IsDir() {
			files++
		}
	}
	if files == 0 {
		t.Fatal("内嵌迁移目录是空的 —— go:embed 可能没生效")
	}
	// 版本号不应超过文件个数（每个迁移一个文件，编号从 1 递增）。
	if got > files {
		t.Fatalf("推导出的版本 %d 大于迁移文件数 %d，编号规则可能被破坏", got, files)
	}
	t.Logf("内嵌迁移 %d 个文件，最高版本 %d", files, got)
}
