package importer

import "strings"

// parsePlainText 处理没有固定结构的文本：
// 每行按空白切分，第一个邮箱作为主邮箱，其余按位置推断。
func parsePlainText(input string) *Result {
	res := &Result{Format: FormatText}
	for i, rawLine := range nonEmptyLines(input) {
		line := stripMetaTail(rawLine)
		parts := strings.FieldsFunc(line, func(r rune) bool {
			return r == ' ' || r == '\t'
		})
		if len(parts) == 0 {
			continue
		}
		r := Row{Line: i + 1}
		assignByPosition(&r, parts)
		res.Rows = append(res.Rows, r)
	}
	return res
}
