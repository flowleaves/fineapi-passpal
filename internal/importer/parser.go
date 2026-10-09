// Package importer 实现批量导入的解析器链。
//
// 每个解析器都是纯函数：只做「原始文本 -> 结构化行」，不碰数据库、不碰网络。
// HTTP 层负责草稿、限额与提交。
package importer

import (
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"unicode/utf8"
)

// 输入上限（doc §10.9）。
const (
	// MaxRawBytes 是原文请求体上限。
	MaxRawBytes = 8 << 20 // 8 MiB
	// MaxRows 是每批记录数上限。
	MaxRows = 10_000
	// MaxFieldBytes 是单字段上限。
	MaxFieldBytes = 64 << 10 // 64 KiB
	// MaxRowBytes 是单行累计上限。
	MaxRowBytes = 128 << 10 // 128 KiB
)

// 解析错误码。返回给前端的只有这些固定码，不回显原文。
const (
	IssueEmailMissing  = "email_missing"
	IssueEmailInvalid  = "email_invalid"
	IssuePasswordEmpty = "password_empty"
	IssueJSONInvalid   = "json_invalid"
	IssueRowMalformed  = "row_malformed"
	IssueTooManyRows   = "too_many_rows"
	IssueFieldTooLarge = "field_too_large"
	IssueRowTooLarge   = "row_too_large"
	IssueEmptyInput    = "empty_input"
	IssueExtraRejected = "extra_rejected"
)

// 识别出的输入格式。
const (
	FormatJSON      = "json"
	FormatCSV       = "csv"
	FormatKeyValue  = "keyvalue"
	FormatDelimiter = "delimiter"
	FormatText      = "text"
)

// Issue 是一条解析问题。Message 只描述问题本身，不含原文片段。
type Issue struct {
	Code    string `json:"code"`
	Field   string `json:"field,omitempty"`
	Message string `json:"message"`
}

// Row 是一条解析结果。字段含明文，只允许存在于内存草稿中。
type Row struct {
	Line           int     `json:"line"`
	Email          string  `json:"email"`
	Username       string  `json:"username"`
	Password       string  `json:"password"`
	BackupEmail    string  `json:"backup_email"`
	F2A            string  `json:"f2a"`
	CredentialJSON string  `json:"credential_json"`
	RefreshToken   string  `json:"refresh_token"`
	SMSLink        string  `json:"sms_link"`
	Notes          string  `json:"notes"`
	Issues         []Issue `json:"issues"`
}

// Result 是一次解析的完整结果。
type Result struct {
	Format string
	Rows   []Row
	Issues []Issue
}

// ErrTooLarge 表示输入超过服务端上限。
var ErrTooLarge = errors.New("输入超过服务端上限")

// Parse 是 AutoDetectParser 入口。
//
// 识别顺序：JSON -> CSV -> Key-Value -> 特殊分隔符 -> 普通文本。
// 明确是 JSON 但语法损坏时报错，不降级成普通文本误识别。
func Parse(input string) (*Result, error) {
	if strings.TrimSpace(input) == "" {
		return nil, &ParseError{Code: IssueEmptyInput, Message: "没有可解析的内容"}
	}
	if len(input) > MaxRawBytes {
		return nil, ErrTooLarge
	}
	if !utf8.ValidString(input) {
		return nil, &ParseError{Code: IssueRowMalformed, Message: "输入不是合法的 UTF-8 文本"}
	}

	// 一行多列导出（账号主体 + tab 分隔的状态/凭证列）先裁掉元数据列，
	// 否则 JSON 里的逗号会让格式识别误判成 CSV。
	// 原始文本仍要传给 finalize —— 凭证提取需要看完整的行。
	body := stripMetaColumns(input)

	trimmed := strings.TrimSpace(body)
	if strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") {
		// 明确 JSON 起点：语法损坏必须报错。
		res, err := parseJSON(body)
		if err != nil {
			return nil, err
		}
		return finalize(res, input)
	}

	if looksLikeCSV(body) {
		if res, err := parseCSV(body); err == nil {
			return finalize(res, input)
		}
		// CSV 解析失败则继续尝试后续格式。
	}

	if looksLikeKeyValue(body) {
		return finalize(parseKeyValue(body), input)
	}

	if _, ok := looksLikeDelimited(body); ok {
		return finalize(parseDelimited(body), input)
	}

	return finalize(parsePlainText(body), input)
}

// stripMetaColumns 逐行裁掉 tab 之后的元数据列，保持行数与空行不变。
func stripMetaColumns(input string) string {
	if !strings.Contains(input, "\t") {
		return input
	}
	lines := strings.Split(strings.ReplaceAll(input, "\r\n", "\n"), "\n")
	for i, line := range lines {
		lines[i] = stripMetaTail(line)
	}
	return strings.Join(lines, "\n")
}

// ParseError 携带固定错误码。
type ParseError struct {
	Code    string
	Message string
}

func (e *ParseError) Error() string { return e.Message }

// finalize 统一做行数与字段大小校验、F2A 归一化，并标注结构性错误。
//
// 对分隔符/文本格式，还会回到原始行里提取 tab 之后的 JSON 凭证 ——
// 这类导出把状态与凭证放在额外列里，第一列才是账号主体。
func finalize(res *Result, input string) (*Result, error) {
	if len(res.Rows) > MaxRows {
		return nil, ErrTooLarge
	}

	var originalLines []string
	if res.Format == FormatDelimiter || res.Format == FormatText {
		originalLines = nonEmptyLines(input)
	}

	for i := range res.Rows {
		r := &res.Rows[i]
		rowBytes := 0
		tooLarge := false
		for _, v := range []string{r.Email, r.Username, r.Password, r.BackupEmail,
			r.F2A, r.CredentialJSON, r.Notes} {
			if len(v) > MaxFieldBytes {
				tooLarge = true
			}
			rowBytes += len(v)
		}
		if tooLarge {
			r.Issues = append(r.Issues, Issue{
				Code: IssueFieldTooLarge, Message: "存在超过单字段上限的值",
			})
		}
		if rowBytes > MaxRowBytes {
			r.Issues = append(r.Issues, Issue{
				Code: IssueRowTooLarge, Message: "该行累计超过单行上限",
			})
		}
		// 邮箱是账号主标识，必须有效非空。
		switch {
		case strings.TrimSpace(r.Email) == "":
			r.Issues = append(r.Issues, Issue{
				Code: IssueEmailMissing, Field: "email", Message: "缺少邮箱",
			})
		case !looksLikeEmail(r.Email):
			r.Issues = append(r.Issues, Issue{
				Code: IssueEmailInvalid, Field: "email", Message: "邮箱格式不正确",
			})
		}
		// 密码为空是有效行上的警告子集，不影响入库。
		if strings.TrimSpace(r.Password) == "" {
			r.Issues = append(r.Issues, Issue{
				Code: IssuePasswordEmpty, Field: "password", Message: "密码为空",
			})
		}

		// 裸 base32 的 F2A 去空格并统一大写，否则复制出去用不了。
		r.F2A = normalizeF2A(r.F2A)

		// 从原始行补全附加列里的 JSON 凭证与 refresh_token。
		if len(originalLines) > 0 && (r.CredentialJSON == "" || r.RefreshToken == "") {
			if idx := r.Line - 1; idx >= 0 && idx < len(originalLines) {
				cj, rt := extractCredentialFromLine(originalLines[idx])
				if r.CredentialJSON == "" && cj != "" {
					r.CredentialJSON = cj
				}
				if r.RefreshToken == "" && rt != "" {
					r.RefreshToken = rt
				}
				if r.SMSLink == "" {
					r.SMSLink = extractSMSLink(originalLines[idx])
				}
			}
		}
	}
	return res, nil
}

// stripMetaTail 去掉 tab 之后的元数据列。
//
// 真实导出里常见这种一行多列：
//
//	email----password----TOTP <TAB> 订阅成功 <TAB> 时间 <TAB> ... <TAB> {"client_id":...}
//
// 第一列才是账号主体，后面的列是状态与凭证。
// 只有当 tab 之前已经是复合主体（含连字符分隔符或 |）时才裁剪；
// 否则 tab 本身就是字段分隔符（email<TAB>password），不能裁。
func stripMetaTail(line string) string {
	idx := strings.IndexByte(line, '\t')
	if idx < 0 {
		return line
	}
	head := line[:idx]
	if dashSplitRe.MatchString(head) || strings.Contains(head, "|") {
		return head
	}
	return line
}

// normalizeF2A 归一化 F2A 值。
//
// otpauth:// URI 原样保留；裸 base32 secret 去掉空格、tab 与连字符并转大写。
func normalizeF2A(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return ""
	}
	if strings.HasPrefix(strings.ToLower(v), "otpauth://") {
		return v
	}
	cleaned := strings.ToUpper(strings.NewReplacer(" ", "", "\t", "", "-", "").Replace(v))
	if f2aSecretRe.MatchString(cleaned) {
		return cleaned
	}
	return v
}

// smsLinkRe 匹配「手机号|取码链接」或裸链接。
var smsLinkRe = regexp.MustCompile(`(?:\d{6,}\|)?https?://[^\s\t"'<>]+`)

// extractSMSLink 从一行的 Tab 元数据列里找出取码链接。
//
// 真实导出的最后一列长这样：
//
//	13800000000|https://card.example.com/api/sms/record?key=xxxx
//
// 只扫描第一列之后的列，且要求整列就是链接（或「手机号|链接」），
// 避免把 JSON 凭证里的 token_uri 误当取码链接。
func extractSMSLink(line string) string {
	cols := strings.Split(line, "\t")
	if len(cols) < 2 {
		return ""
	}
	for _, col := range cols[1:] {
		col = strings.TrimSpace(col)
		if col == "" || strings.HasPrefix(col, "{") {
			continue
		}
		m := smsLinkRe.FindString(col)
		if m == "" {
			continue
		}
		// 整列就是链接，或整列是「手机号|链接」
		if m == col || strings.HasPrefix(col, m) {
			return col
		}
	}
	return ""
}

// isRefreshToken 判断是否像 OAuth refresh token（Google 的以 1// 开头且很长）。
func isRefreshToken(s string) bool {
	return len(s) >= 20 && strings.HasPrefix(s, "1//")
}

// credentialHints 是判断一个 JSON 对象是否为凭据的特征键。
var credentialHints = []string{
	"client_id", "client_secret", "private_key", "token_uri", "refresh_token", "project_id",
}

// extractCredentialFromLine 从一行里找出 JSON 凭证，并顺带取出其中的 refresh_token。
//
// 只做整体对象解析，不做正则替换，因此不会把原文片段写进数据库。
func extractCredentialFromLine(line string) (credJSON, refreshToken string) {
	start := strings.IndexByte(line, '{')
	end := strings.LastIndexByte(line, '}')
	if start < 0 || end <= start {
		return "", ""
	}
	raw := line[start : end+1]
	if len(raw) > MaxFieldBytes {
		return "", ""
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(raw), &obj); err != nil {
		return "", ""
	}
	hit := false
	for _, hint := range credentialHints {
		if _, ok := obj[hint]; ok {
			hit = true
			break
		}
	}
	if !hit {
		return "", ""
	}
	if v, ok := obj["refresh_token"].(string); ok {
		refreshToken = strings.TrimSpace(v)
	}
	return raw, refreshToken
}

// 字段识别辅助。

var (
	emailRe   = regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9](?:[A-Za-z0-9\-]*[A-Za-z0-9])?(?:\.[A-Za-z0-9](?:[A-Za-z0-9\-]*[A-Za-z0-9])?)+`)
	otpauthRe = regexp.MustCompile(`otpauth://[^\s"',]+`)
	// base32 TOTP secret：16~64 位大写字母与数字 2-7，可带 = 填充。
	f2aSecretRe = regexp.MustCompile(`^[A-Z2-7]{16,64}={0,6}$`)
)

// findEmail 返回字符串中第一个邮箱。
func findEmail(s string) string {
	return emailRe.FindString(s)
}

// isF2AValue 判断是否像一个 TOTP secret 或 otpauth URI。
func isF2AValue(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return false
	}
	if strings.HasPrefix(strings.ToLower(s), "otpauth://") {
		return true
	}
	return f2aSecretRe.MatchString(strings.ToUpper(s))
}

// normalizeKey 把各种写法收敛成小写无分隔形式，便于查表。
func normalizeKey(k string) string {
	k = strings.ToLower(strings.TrimSpace(k))
	repl := strings.NewReplacer("_", "", "-", "", " ", "", ".", "")
	return repl.Replace(k)
}

// 字段别名表。键为 normalizeKey 之后的形式。
var fieldAliases = map[string]string{
	"email":          "email",
	"mail":           "email",
	"邮箱":             "email",
	"账号":             "email",
	"account":        "email",
	"username":       "username",
	"user":           "username",
	"login":          "username",
	"用户名":            "username",
	"password":       "password",
	"pass":           "password",
	"pwd":            "password",
	"密码":             "password",
	"backupemail":    "backup_email",
	"backup":         "backup_email",
	"recovery":       "backup_email",
	"recoveryemail":  "backup_email",
	"辅助邮箱":           "backup_email",
	"备用邮箱":           "backup_email",
	"f2a":            "f2a",
	"2fa":            "f2a",
	"totp":           "f2a",
	"otp":            "f2a",
	"authenticator":  "f2a",
	"验证码密钥":          "f2a",
	"验证码":            "f2a",
	"credentialjson": "credential_json",
	"credentials":    "credential_json",
	"credential":     "credential_json",
	"json":           "credential_json",
	"凭证":             "credential_json",
	"rt":             "refresh_token",
	"refreshtoken":   "refresh_token",
	"refresh":        "refresh_token",
	"刷新令牌":           "refresh_token",
	"smslink":        "sms_link",
	"sms":            "sms_link",
	"取码链接":           "sms_link",
	"接码链接":           "sms_link",
	"取码":             "sms_link",
	"notes":          "notes",
	"note":           "notes",
	"remark":         "notes",
	"备注":             "notes",
}

// resolveField 把别名解析成标准字段名。
func resolveField(key string) (string, bool) {
	v, ok := fieldAliases[normalizeKey(key)]
	return v, ok
}

// looksLikeEmail 做一次宽松的邮箱判断（不依赖正则性能）。
//
// 含分隔符特征的串一律不是邮箱 —— 那说明整行没被切开（分隔符没识别出来），
// 放行会让「邮箱」字段塞进整行原文，最终报一个莫名其妙的 email_invalid。
func looksLikeEmail(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" || strings.ContainsAny(s, " \t") {
		return false
	}
	if strings.Contains(s, "---") || strings.Contains(s, "|") {
		return false
	}
	at := strings.LastIndex(s, "@")
	if at <= 0 || at == len(s)-1 {
		return false
	}
	domain := s[at+1:]
	return strings.Contains(domain, ".") && !strings.HasPrefix(domain, ".") && !strings.HasSuffix(domain, ".")
}
