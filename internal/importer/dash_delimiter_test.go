package importer

import "testing"

// sampleTripleDashRow 用**合成**数据还原用户反馈的一类导出格式：
// 用三个连字符分隔，字段顺序是 邮箱---密码---F2A---RefreshToken。
//
// 与 README 里的 `email----password----backup_email----f2a` 有两处不同：
//   - 分隔符是 3 个连字符，不是 4 个
//   - 第 3 段是 32 位 base32（TOTP secret）而非辅助邮箱；
//     第 4 段是 `1//` 开头的 refresh token 而非 F2A
//
// 注意：这里必须用假数据。仓库启用了 GitHub Push Protection，
// 一旦提交真实凭据会被直接拦下（且本就绝不该入库）。
const sampleTripleDashRow = "sample@example.com---SamplePass---" +
	"JBSWY3DPEHPK3PXP---" +
	"1//0000000000000000000000000000000000000000"

const sampleRefreshToken = "1//0000000000000000000000000000000000000000"

func TestParseTripleDashDelimiter(t *testing.T) {
	res, err := Parse(sampleTripleDashRow)
	if err != nil {
		t.Fatalf("Parse 失败：%v", err)
	}
	if res.Format != FormatDelimiter {
		t.Fatalf("格式应为 delimiter，实际 %s", res.Format)
	}
	if len(res.Rows) != 1 {
		t.Fatalf("应有 1 行，实际 %d", len(res.Rows))
	}

	r := res.Rows[0]
	if r.Email != "sample@example.com" {
		t.Fatalf("邮箱错误：%q", r.Email)
	}
	if r.Password != "SamplePass" {
		t.Fatalf("密码错误：%q", r.Password)
	}
	if r.BackupEmail != "" {
		t.Fatalf("辅助邮箱应为空，实际 %q", r.BackupEmail)
	}
	if r.F2A != "JBSWY3DPEHPK3PXP" {
		t.Fatalf("F2A 错误：%q", r.F2A)
	}
	if r.RefreshToken != sampleRefreshToken {
		t.Fatalf("refresh_token 错误：%q", r.RefreshToken)
	}
	if len(r.Issues) != 0 {
		t.Fatalf("不应有任何问题标记，实际 %+v", r.Issues)
	}
}

// 三个连字符必须排在四个之后检测，否则四连字符会被切成
// a / 空 / b，凭空多出一个空列。
func TestFourDashStillWinsOverThreeDash(t *testing.T) {
	res, err := Parse("a@x.com----p1----b@x.com----JBSWY3DPEHPK3PXP\n")
	if err != nil {
		t.Fatalf("Parse 失败：%v", err)
	}
	if res.Format != FormatDelimiter {
		t.Fatalf("格式应为 delimiter，实际 %s", res.Format)
	}
	r := res.Rows[0]
	if r.Email != "a@x.com" || r.Password != "p1" ||
		r.BackupEmail != "b@x.com" || r.F2A != "JBSWY3DPEHPK3PXP" {
		t.Fatalf("四连字符分隔解析错误：%+v", r)
	}
}

// 连字符个数在不同导出里不统一。按固定长度切会让 5 个连字符被 4 个的规则切开，
// 在下一字段留下前导 `-`（密码变成 `-p1`）。
func TestLongerDashRunSplitsCleanly(t *testing.T) {
	res, err := Parse("a@x.com-----p1-----JBSWY3DPEHPK3PXP\n")
	if err != nil {
		t.Fatalf("Parse 失败：%v", err)
	}
	if res.Format != FormatDelimiter {
		t.Fatalf("格式应为 delimiter，实际 %s", res.Format)
	}
	r := res.Rows[0]
	if r.Email != "a@x.com" || r.Password != "p1" || r.F2A != "JBSWY3DPEHPK3PXP" {
		t.Fatalf("五连字符分隔解析错误：%+v", r)
	}
}

// 多行都要正确对应，不能串行。
func TestTripleDashMultipleRows(t *testing.T) {
	input := "a@x.com---p1---JBSWY3DPEHPK3PXP\n" +
		"b@x.com---p2---KRSXG5CTMVRXEZLU\n"
	res, err := Parse(input)
	if err != nil {
		t.Fatalf("Parse 失败：%v", err)
	}
	if len(res.Rows) != 2 {
		t.Fatalf("应有 2 行，实际 %d", len(res.Rows))
	}
	if res.Rows[0].Email != "a@x.com" || res.Rows[0].Password != "p1" ||
		res.Rows[0].F2A != "JBSWY3DPEHPK3PXP" {
		t.Fatalf("第一行错误：%+v", res.Rows[0])
	}
	if res.Rows[1].Email != "b@x.com" || res.Rows[1].Password != "p2" ||
		res.Rows[1].F2A != "KRSXG5CTMVRXEZLU" {
		t.Fatalf("第二行错误：%+v", res.Rows[1])
	}
}

// 密码里的单个连字符不能被当成分隔符。
func TestSingleDashInPasswordKept(t *testing.T) {
	res, err := Parse("a@x.com---p-1---JBSWY3DPEHPK3PXP\n")
	if err != nil {
		t.Fatalf("Parse 失败：%v", err)
	}
	if res.Rows[0].Password != "p-1" {
		t.Fatalf("单个连字符应保留在密码里，实际 %q", res.Rows[0].Password)
	}
}

// 同一批数据混用 `----` 与 `|`：每行要按自己的分隔符切，不能整批共用一个 ——
// 否则少数派那几行会整行挤进一个字段（表现为「缺少邮箱」，或更糟：
// 整行被宽松的邮箱判定放行，当成邮箱存进库）。
func TestMixedDelimitersInOneBatch(t *testing.T) {
	input := "u1@gmail.com----Pass1!\n" +
		"u2@gmail.com----Pass2!----backup2@gmail.com----JBSWY3DPEHPK3PXP\n" +
		"u3@gmail.com|Pass3!|backup3@qq.com\n"
	res, err := Parse(input)
	if err != nil {
		t.Fatalf("Parse 失败：%v", err)
	}
	if res.Format != FormatDelimiter {
		t.Fatalf("格式应为 delimiter，实际 %s", res.Format)
	}
	if len(res.Rows) != 3 {
		t.Fatalf("应有 3 行，实际 %d", len(res.Rows))
	}

	r3 := res.Rows[2]
	if r3.Email != "u3@gmail.com" {
		t.Fatalf("竖线行邮箱错误：%q", r3.Email)
	}
	if r3.Password != "Pass3!" {
		t.Fatalf("竖线行密码错误：%q", r3.Password)
	}
	if r3.BackupEmail != "backup3@qq.com" {
		t.Fatalf("竖线行辅助邮箱错误：%q", r3.BackupEmail)
	}
	if len(r3.Issues) != 0 {
		t.Fatalf("竖线行不应有问题码：%+v", r3.Issues)
	}
}
