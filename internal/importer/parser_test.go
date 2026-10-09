package importer

import (
	"strconv"
	"strings"
	"testing"
)

// 解析器是纯函数，可以在不碰数据库的前提下完整覆盖。

func TestParseJSONArray(t *testing.T) {
	input := `[
      {"email":"a@x.com","password":"p1","backup_email":"b@x.com","f2a":"JBSWY3DPEHPK3PXP"},
      {"email":"b@x.com","password":"p2"}
    ]`
	res, err := Parse(input)
	if err != nil {
		t.Fatalf("Parse 失败：%v", err)
	}
	if res.Format != FormatJSON {
		t.Fatalf("格式应为 json，实际 %s", res.Format)
	}
	if len(res.Rows) != 2 {
		t.Fatalf("应有 2 行，实际 %d", len(res.Rows))
	}
	if res.Rows[0].Email != "a@x.com" || res.Rows[0].Password != "p1" {
		t.Fatalf("第一行解析错误：%+v", res.Rows[0])
	}
	if res.Rows[0].BackupEmail != "b@x.com" || res.Rows[0].F2A != "JBSWY3DPEHPK3PXP" {
		t.Fatalf("第一行辅助字段解析错误：%+v", res.Rows[0])
	}
}

func TestParseBrokenJSONDoesNotFallBack(t *testing.T) {
	// 明确是 JSON 起点但语法损坏：必须报错，不能降级成普通文本。
	_, err := Parse(`{"email": "a@x.com", `)
	if err == nil {
		t.Fatal("损坏的 JSON 应当返回错误")
	}
	pe, ok := err.(*ParseError)
	if !ok || pe.Code != IssueJSONInvalid {
		t.Fatalf("应返回 json_invalid，实际 %v", err)
	}
}

func TestParseCSVWithHeader(t *testing.T) {
	input := "email,password,backup_email\n" +
		"a@x.com,p1,b@x.com\n" +
		"c@x.com,p2,\n"
	res, err := Parse(input)
	if err != nil {
		t.Fatalf("Parse 失败：%v", err)
	}
	if res.Format != FormatCSV {
		t.Fatalf("格式应为 csv，实际 %s", res.Format)
	}
	if len(res.Rows) != 2 {
		t.Fatalf("应有 2 行，实际 %d", len(res.Rows))
	}
	if res.Rows[0].Email != "a@x.com" || res.Rows[0].BackupEmail != "b@x.com" {
		t.Fatalf("表头映射错误：%+v", res.Rows[0])
	}
	if res.Rows[1].BackupEmail != "" {
		t.Fatalf("空字段应保持为空：%+v", res.Rows[1])
	}
}

func TestParseCSVQuotedFields(t *testing.T) {
	input := "email,password\n" +
		"\"a@x.com\",\"pa,ss\"\n"
	res, err := Parse(input)
	if err != nil {
		t.Fatalf("Parse 失败：%v", err)
	}
	if len(res.Rows) != 1 || res.Rows[0].Password != "pa,ss" {
		t.Fatalf("引号内的逗号应被正确处理：%+v", res.Rows)
	}
}

func TestParseKeyValue(t *testing.T) {
	input := "email: a@x.com\n" +
		"password: p1\n" +
		"2fa: JBSWY3DPEHPK3PXP\n"
	res, err := Parse(input)
	if err != nil {
		t.Fatalf("Parse 失败：%v", err)
	}
	if res.Format != FormatKeyValue {
		t.Fatalf("格式应为 keyvalue，实际 %s", res.Format)
	}
	if len(res.Rows) != 1 {
		t.Fatalf("应有 1 行，实际 %d", len(res.Rows))
	}
	r := res.Rows[0]
	if r.Email != "a@x.com" || r.Password != "p1" || r.F2A != "JBSWY3DPEHPK3PXP" {
		t.Fatalf("Key-Value 解析错误：%+v", r)
	}
}

func TestParseKeyValueMultipleBlocks(t *testing.T) {
	input := "email: a@x.com\npassword: p1\n\nemail: b@x.com\npassword: p2\n"
	res, err := Parse(input)
	if err != nil {
		t.Fatalf("Parse 失败：%v", err)
	}
	if len(res.Rows) != 2 {
		t.Fatalf("空行分隔应产生 2 行，实际 %d", len(res.Rows))
	}
}

func TestParseDelimiterFourFields(t *testing.T) {
	input := "a@x.com----p1----b@x.com----JBSWY3DPEHPK3PXP\n" +
		"c@x.com----p2\n"
	res, err := Parse(input)
	if err != nil {
		t.Fatalf("Parse 失败：%v", err)
	}
	if res.Format != FormatDelimiter {
		t.Fatalf("格式应为 delimiter，实际 %s", res.Format)
	}
	r := res.Rows[0]
	if r.Email != "a@x.com" || r.Password != "p1" || r.BackupEmail != "b@x.com" || r.F2A != "JBSWY3DPEHPK3PXP" {
		t.Fatalf("四字段分隔解析错误：%+v", r)
	}
}

func TestParseDelimiterPipe(t *testing.T) {
	input := "a@x.com|p1|b@x.com\n"
	res, err := Parse(input)
	if err != nil {
		t.Fatalf("Parse 失败：%v", err)
	}
	if len(res.Rows) != 1 || res.Rows[0].BackupEmail != "b@x.com" {
		t.Fatalf("竖线分隔解析错误：%+v", res.Rows)
	}
}

func TestParsePlainTextSpaceSeparated(t *testing.T) {
	res, err := Parse("a@x.com p1\n")
	if err != nil {
		t.Fatalf("Parse 失败：%v", err)
	}
	if len(res.Rows) != 1 || res.Rows[0].Email != "a@x.com" || res.Rows[0].Password != "p1" {
		t.Fatalf("普通文本解析错误：%+v", res.Rows)
	}
}

func TestParseOtpauthStaysIntact(t *testing.T) {
	uri := "otpauth://totp/Google:a@x.com?secret=JBSWY3DPEHPK3PXP&issuer=Google"
	res, err := Parse("email: a@x.com\npassword: p1\nf2a: " + uri + "\n")
	if err != nil {
		t.Fatalf("Parse 失败：%v", err)
	}
	if res.Rows[0].F2A != uri {
		t.Fatalf("otpauth URI 应原样保留，实际 %q", res.Rows[0].F2A)
	}
}

func TestMissingEmailIsMarkedInvalid(t *testing.T) {
	res, err := Parse("not-an-email----p1\n")
	if err != nil {
		t.Fatalf("Parse 失败：%v", err)
	}
	if len(res.Rows) != 1 {
		t.Fatalf("应有 1 行，实际 %d", len(res.Rows))
	}
	if !hasCode(res.Rows[0].Issues, IssueEmailMissing) {
		t.Fatalf("缺少邮箱应被标记，实际 %+v", res.Rows[0].Issues)
	}
}

func TestEmptyPasswordIsWarningNotInvalid(t *testing.T) {
	res, err := Parse("a@x.com\n")
	if err != nil {
		t.Fatalf("Parse 失败：%v", err)
	}
	r := res.Rows[0]
	if !hasCode(r.Issues, IssuePasswordEmpty) {
		t.Fatalf("空密码应给出警告，实际 %+v", r.Issues)
	}
	for _, is := range r.Issues {
		switch is.Code {
		case IssueEmailMissing, IssueEmailInvalid:
			t.Fatalf("空密码不应导致邮箱类错误：%+v", r.Issues)
		}
	}
}

func TestEmptyInputRejected(t *testing.T) {
	if _, err := Parse("   \n  \n"); err == nil {
		t.Fatal("空输入应被拒绝")
	}
}

func TestTooManyRowsRejected(t *testing.T) {
	var sb strings.Builder
	for i := 0; i <= MaxRows; i++ {
		sb.WriteString("a")
		sb.WriteString(strconv.Itoa(i))
		sb.WriteString("@x.com----p\n")
	}
	if _, err := Parse(sb.String()); err != ErrTooLarge {
		t.Fatalf("超过行数上限应返回 ErrTooLarge，实际 %v", err)
	}
}

// ── 真实导出格式（已脱敏）────────────────────────────────────
//
// 格式 A：一行多列，第 1 列是账号主体，其余列是状态与凭证。
// 格式 B：---- 分列，中间有空字段，末尾是 refresh_token。

const sampleMetaRow = "SampleA@example.com----abc123pass----BSTX 7X2H ZBRS FZHK 4EQP IS7L UPW4 MNY5\t订阅成功\t2026-10-05 15:38:40\t\t\t\t账号凭证获取成功\t{\"client_id\": \"demo.apps.googleusercontent.com\", \"client_secret\": \"DEMO-SECRET\", \"token_uri\": \"https://oauth2.googleapis.com/token\", \"refresh_token\": \"1//0demoREFRESHtokenVALUE1234567890abc\", \"project_id\": \"demo-project\"}\t反重力验证成功\t13800000000|https://example.com/sms?key=demo"

const sampleEmptyFieldRow = "SampleB@example.com----pw2pass--------TDWD7C7J57V535PKHS5ZGKWYA5XBAMZ3----1//0demoRefreshTokenBbbbbbbbbbbbbbbbbbbbbb"

func TestParseMetaColumnsFormat(t *testing.T) {
	res, err := Parse(sampleMetaRow)
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

	if r.Email != "SampleA@example.com" {
		t.Fatalf("邮箱错误：%q", r.Email)
	}
	if r.Password != "abc123pass" {
		t.Fatalf("密码错误：%q", r.Password)
	}
	// tab 之后的元数据不能被并进 F2A
	if r.F2A != "BSTX7X2HZBRSFZHK4EQPIS7LUPW4MNY5" {
		t.Fatalf("F2A 应去掉空格并大写，实际 %q", r.F2A)
	}
	if strings.Contains(r.F2A, "订阅成功") || strings.Contains(r.F2A, "2026-") {
		t.Fatalf("F2A 混入了元数据列：%q", r.F2A)
	}
	// 附加列里的 JSON 凭证要被抓出来
	if !strings.Contains(r.CredentialJSON, "demo.apps.googleusercontent.com") {
		t.Fatalf("JSON 凭证未提取：%q", r.CredentialJSON)
	}
	if strings.Contains(r.CredentialJSON, "订阅成功") || strings.Contains(r.CredentialJSON, "13800000000") {
		t.Fatalf("JSON 凭证里混入了状态列：%q", r.CredentialJSON)
	}
	if r.RefreshToken != "1//0demoREFRESHtokenVALUE1234567890abc" {
		t.Fatalf("refresh_token 未从 JSON 中取出：%q", r.RefreshToken)
	}
	// 末尾元数据列里的取码链接
	if r.SMSLink != "13800000000|https://example.com/sms?key=demo" {
		t.Fatalf("取码链接未提取：%q", r.SMSLink)
	}
}

func TestSMSLinkDoesNotGrabTokenURI(t *testing.T) {
	// JSON 凭证里的 token_uri 不能被当成取码链接
	input := "a@x.com----p----TDWD7C7J57V535PKHS5ZGKWYA5XBAMZ3" +
		"\t{\"token_uri\": \"https://oauth2.googleapis.com/token\"}"
	res, err := Parse(input)
	if err != nil {
		t.Fatalf("Parse 失败：%v", err)
	}
	if res.Rows[0].SMSLink != "" {
		t.Fatalf("不应把 JSON 里的 token_uri 当取码链接：%q", res.Rows[0].SMSLink)
	}
}

func TestSMSLinkViaKeyValue(t *testing.T) {
	res, err := Parse("email: a@x.com\npassword: p\nsms_link: 13800000000|https://example.com/sms?key=k\n")
	if err != nil {
		t.Fatalf("Parse 失败：%v", err)
	}
	if res.Rows[0].SMSLink != "13800000000|https://example.com/sms?key=k" {
		t.Fatalf("sms_link 键未识别：%q", res.Rows[0].SMSLink)
	}
}

func TestSMSLinkViaJSON(t *testing.T) {
	res, err := Parse(`[{"email":"a@x.com","password":"p","sms_link":"13800000000|https://example.com/sms?key=j"}]`)
	if err != nil {
		t.Fatalf("Parse 失败：%v", err)
	}
	if res.Rows[0].SMSLink != "13800000000|https://example.com/sms?key=j" {
		t.Fatalf("JSON 里的 sms_link 未识别：%q", res.Rows[0].SMSLink)
	}
}

func TestParseEmptyFieldAndRefreshToken(t *testing.T) {
	res, err := Parse(sampleEmptyFieldRow)
	if err != nil {
		t.Fatalf("Parse 失败：%v", err)
	}
	if len(res.Rows) != 1 {
		t.Fatalf("应有 1 行，实际 %d", len(res.Rows))
	}
	r := res.Rows[0]

	if r.Email != "SampleB@example.com" {
		t.Fatalf("邮箱错误：%q", r.Email)
	}
	if r.Password != "pw2pass" {
		t.Fatalf("密码错误：%q", r.Password)
	}
	// 连续分隔符产生的空字段必须跳过，不能被当成值
	if r.BackupEmail != "" {
		t.Fatalf("空的辅助邮箱应保持为空，实际 %q", r.BackupEmail)
	}
	if r.F2A != "TDWD7C7J57V535PKHS5ZGKWYA5XBAMZ3" {
		t.Fatalf("F2A 错误：%q", r.F2A)
	}
	if r.RefreshToken != "1//0demoRefreshTokenBbbbbbbbbbbbbbbbbbbbbb" {
		t.Fatalf("refresh_token 错误：%q", r.RefreshToken)
	}
}

func TestParseMetaColumnsMultipleRows(t *testing.T) {
	// 两行都要正确对应，不能串行
	second := strings.Replace(sampleMetaRow, "SampleA@example.com", "SecondB@example.com", 1)
	res, err := Parse(sampleMetaRow + "\n" + second)
	if err != nil {
		t.Fatalf("Parse 失败：%v", err)
	}
	if len(res.Rows) != 2 {
		t.Fatalf("应有 2 行，实际 %d", len(res.Rows))
	}
	if res.Rows[0].Email != "SampleA@example.com" || res.Rows[1].Email != "SecondB@example.com" {
		t.Fatalf("行与凭证串位：%q / %q", res.Rows[0].Email, res.Rows[1].Email)
	}
	for i, r := range res.Rows {
		if !strings.Contains(r.CredentialJSON, "demo.apps.googleusercontent.com") {
			t.Fatalf("第 %d 行的凭证未提取：%q", i+1, r.CredentialJSON)
		}
	}
}

func TestStripMetaTailKeepsPlainTabSeparated(t *testing.T) {
	// email<TAB>password 这种 tab 就是字段分隔符，不能裁掉后半段
	got := stripMetaTail("a@x.com\tp1")
	if got != "a@x.com\tp1" {
		t.Fatalf("普通 tab 分隔不应被裁剪，实际 %q", got)
	}
	// 含复合主体的才裁
	got = stripMetaTail("a@x.com----p1\t元数据")
	if got != "a@x.com----p1" {
		t.Fatalf("复合主体后的元数据应被裁剪，实际 %q", got)
	}
}

func TestNormalizeF2AKeepsOtpauth(t *testing.T) {
	uri := "otpauth://totp/Google:a@x.com?secret=JBSWY3DPEHPK3PXP&issuer=Google"
	if got := normalizeF2A(uri); got != uri {
		t.Fatalf("otpauth URI 应原样保留，实际 %q", got)
	}
	if got := normalizeF2A("jbs w y3dp ehpk 3pxp"); got != "JBSWY3DPEHPK3PXP" {
		t.Fatalf("裸 base32 应去空格并大写，实际 %q", got)
	}
}

func TestJSONRefreshToken(t *testing.T) {
	input := `[{"email":"a@x.com","password":"p","refresh_token":"1//0jsonRTvalue1234567890abcdef"}]`
	res, err := Parse(input)
	if err != nil {
		t.Fatalf("Parse 失败：%v", err)
	}
	if res.Rows[0].RefreshToken != "1//0jsonRTvalue1234567890abcdef" {
		t.Fatalf("JSON 里的 refresh_token 未识别：%q", res.Rows[0].RefreshToken)
	}
}

func TestKeyValueRefreshToken(t *testing.T) {
	res, err := Parse("email: a@x.com\npassword: p\nrt: 1//0kvRTvalue1234567890abcdefgh\n")
	if err != nil {
		t.Fatalf("Parse 失败：%v", err)
	}
	if res.Rows[0].RefreshToken != "1//0kvRTvalue1234567890abcdefgh" {
		t.Fatalf("rt 键未识别：%q", res.Rows[0].RefreshToken)
	}
}

func hasCode(issues []Issue, code string) bool {
	for _, is := range issues {
		if is.Code == code {
			return true
		}
	}
	return false
}
