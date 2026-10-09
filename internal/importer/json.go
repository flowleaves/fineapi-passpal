package importer

import (
	"encoding/json"
	"strconv"
	"strings"
)

// parseJSON 解析 JSON 数组、单对象，或 {"accounts": [...]} 包装。
//
// 明确以 { 或 [ 开头但语法损坏时返回 ParseError，不降级成普通文本。
func parseJSON(input string) (*Result, error) {
	trimmed := strings.TrimSpace(input)
	res := &Result{Format: FormatJSON}

	if strings.HasPrefix(trimmed, "[") {
		var arr []map[string]any
		if err := json.Unmarshal([]byte(trimmed), &arr); err != nil {
			return nil, &ParseError{Code: IssueJSONInvalid, Message: "JSON 数组语法错误"}
		}
		for i, obj := range arr {
			res.Rows = append(res.Rows, rowFromMap(obj, i+1))
		}
		return res, nil
	}

	var obj map[string]any
	if err := json.Unmarshal([]byte(trimmed), &obj); err != nil {
		return nil, &ParseError{Code: IssueJSONInvalid, Message: "JSON 对象语法错误"}
	}
	if raw, ok := obj["accounts"]; ok {
		if arr, ok := raw.([]any); ok {
			for i, item := range arr {
				if m, ok := item.(map[string]any); ok {
					res.Rows = append(res.Rows, rowFromMap(m, i+1))
				}
			}
			return res, nil
		}
	}
	res.Rows = append(res.Rows, rowFromMap(obj, 1))
	return res, nil
}

func rowFromMap(m map[string]any, line int) Row {
	r := Row{Line: line}
	for k, v := range m {
		field, ok := resolveField(k)
		if !ok {
			continue
		}
		assign(&r, field, valueToString(v))
	}
	return r
}

func valueToString(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(t)
	case bool:
		return strconv.FormatBool(t)
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case json.Number:
		return t.String()
	default:
		b, err := json.Marshal(v)
		if err != nil {
			return ""
		}
		return string(b)
	}
}

// assign 按字段名写入行。
func assign(r *Row, field, value string) {
	value = strings.TrimSpace(value)
	if value == "" {
		return
	}
	switch field {
	case "email":
		r.Email = value
	case "username":
		r.Username = value
	case "password":
		r.Password = value
	case "backup_email":
		r.BackupEmail = value
	case "f2a":
		r.F2A = value
	case "credential_json":
		r.CredentialJSON = value
	case "notes":
		r.Notes = value
	case "refresh_token":
		r.RefreshToken = value
	case "sms_link":
		r.SMSLink = value
	}
}

// assignByPosition 在无语义标签时按位置推断字段。
//
// 规则（doc §10.4）：第一个邮箱是主邮箱，第二个邮箱是辅助邮箱，
// otpauth:// 一定归 F2A，其余按顺序填入密码与 F2A。
func assignByPosition(r *Row, parts []string) {
	emailSeen := 0
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		switch {
		case looksLikeEmail(p):
			if emailSeen == 0 {
				r.Email = p
			} else if r.BackupEmail == "" {
				r.BackupEmail = p
			}
			emailSeen++
		case strings.HasPrefix(strings.ToLower(p), "otpauth://"):
			if r.F2A == "" {
				r.F2A = p
			}
		case isRefreshToken(p):
			// Google 的 refresh token 以 1// 开头且很长，位置不固定。
			if r.RefreshToken == "" {
				r.RefreshToken = p
			}
		case r.Password == "":
			r.Password = p
		case r.F2A == "":
			r.F2A = p
		case r.RefreshToken == "":
			r.RefreshToken = p
		default:
			// 多余列直接忽略，不猜测。
		}
	}
}
