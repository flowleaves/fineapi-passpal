package importer

import "strings"

// 分隔符候选，长分隔符优先，避免 "----" 被 "-" 抢先。
var delimiterCandidates = []string{"----", "|", "\t", ";"}

// detectDelimiter 找出多数行都含有的分隔符。
func detectDelimiter(input string) (string, bool) {
	lines := nonEmptyLines(input)
	if len(lines) == 0 {
		return "", false
	}
	for _, sep := range delimiterCandidates {
		count := 0
		for _, l := range lines {
			if strings.Contains(l, sep) {
				count++
			}
		}
		if count > 0 && count*2 >= len(lines) {
			return sep, true
		}
	}
	return "", false
}

// parseDelimited 按分隔符切列，再按位置推断字段。
func parseDelimited(input, sep string) *Result {
	res := &Result{Format: FormatDelimiter}
	for i, rawLine := range nonEmptyLines(input) {
		// 一行多列导出里，tab 之后是状态/凭证等元数据，不属于主体。
		line := stripMetaTail(rawLine)
		parts := strings.Split(line, sep)
		for j := range parts {
			parts[j] = strings.TrimSpace(parts[j])
		}
		r := Row{Line: i + 1}
		assignByPosition(&r, parts)
		res.Rows = append(res.Rows, r)
	}
	return res
}
