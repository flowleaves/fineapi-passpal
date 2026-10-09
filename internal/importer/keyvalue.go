package importer

import "strings"

// looksLikeKeyValue 判断输入是否以 key: value 为主。
func looksLikeKeyValue(input string) bool {
	lines := nonEmptyLines(input)
	if len(lines) == 0 {
		return false
	}
	matched := 0
	for _, l := range lines {
		k, _, ok := splitKeyValue(l)
		if !ok {
			continue
		}
		if _, ok := resolveField(k); ok {
			matched++
		}
	}
	return matched >= 2
}

// splitKeyValue 拆出 "键: 值" 或 "键=值"。键长度受限，避免把正文误判成键。
func splitKeyValue(line string) (key, value string, ok bool) {
	idx := strings.IndexAny(line, ":：=")
	if idx <= 0 || idx == len(line)-1 {
		return "", "", false
	}
	key = strings.TrimSpace(line[:idx])
	value = strings.TrimSpace(line[idx+1:])
	if key == "" || value == "" || len([]rune(key)) > 32 {
		return "", "", false
	}
	// 键不应含空白，避免把 "some sentence: text" 当成字段。
	if strings.ContainsAny(key, " \t") {
		return "", "", false
	}
	return key, value, true
}

// parseKeyValue 解析 Key-Value 文本。
//
// 分块策略：优先按空行分块；若无空行，则遇到第二个 email 键时另起一块。
func parseKeyValue(input string) *Result {
	res := &Result{Format: FormatKeyValue}

	var blocks [][]string
	var cur []string
	for _, line := range strings.Split(strings.ReplaceAll(input, "\r\n", "\n"), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			if len(cur) > 0 {
				blocks = append(blocks, cur)
				cur = nil
			}
			continue
		}
		cur = append(cur, trimmed)
	}
	if len(cur) > 0 {
		blocks = append(blocks, cur)
	}

	// 单块但含多个 email 键时，按 email 重新切分。
	if len(blocks) == 1 {
		blocks = splitByEmailKey(blocks[0])
	}

	for i, block := range blocks {
		r := Row{Line: i + 1}
		for _, line := range block {
			k, v, ok := splitKeyValue(line)
			if !ok {
				continue
			}
			field, ok := resolveField(k)
			if !ok {
				continue
			}
			assign(&r, field, v)
		}
		if r.Email != "" || r.Password != "" || r.F2A != "" {
			res.Rows = append(res.Rows, r)
		}
	}
	return res
}

// splitByEmailKey 在遇到第二个 email 键时切块。
func splitByEmailKey(lines []string) [][]string {
	var blocks [][]string
	var cur []string
	seenEmail := false
	for _, line := range lines {
		k, _, ok := splitKeyValue(line)
		isEmail := false
		if ok {
			if f, ok2 := resolveField(k); ok2 && f == "email" {
				isEmail = true
			}
		}
		if isEmail && seenEmail {
			blocks = append(blocks, cur)
			cur = nil
		}
		if isEmail {
			seenEmail = true
		}
		cur = append(cur, line)
	}
	if len(cur) > 0 {
		blocks = append(blocks, cur)
	}
	return blocks
}
