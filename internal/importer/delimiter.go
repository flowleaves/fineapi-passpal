package importer

import (
	"regexp"
	"strings"
)

// 分隔符候选，长分隔符优先。
//
// `---`（3 个连字符）必须排在 `----`（4 个）之后：反过来的话 `a----b`
// 会被切成 a / 空 / b，凭空多出一个空列。
// 两种长度都要有 —— 不同导出工具用的连字符个数不一样，
// 只认 `----` 会让用 `---` 的数据识别不出来，降级成普通文本。
var delimiterCandidates = []string{"----", "---", "|", "\t", ";"}

// dashSplitRe 匹配 3 个及以上连续连字符。
//
// 连字符的个数在不同导出里并不统一（3 / 4 / 5 …）。若按固定长度切，
// 5 个连字符会被 4 个的规则切开，在下一字段留下前导 `-`（如密码变成 `-p1`）。
// 统一按「3 个及以上」切即可覆盖全部长度。
var dashSplitRe = regexp.MustCompile(`-{3,}`)

// dashSeparator 是连字符族在 pickLineSeparator 里的代表值。
// 它不是真实分隔符，命中后要用 dashSplitRe 切。
const dashSeparator = "dash"

// pickLineSeparator 为**单行**挑选分隔符。
//
// 连字符族优先：`---` 这类连续连字符几乎不会出现在密码等内容里，
// 而 `|` / `;` 有可能。返回 dashSeparator 表示要用 dashSplitRe 切。
func pickLineSeparator(line string) (string, bool) {
	if dashSplitRe.MatchString(line) {
		return dashSeparator, true
	}
	for _, sep := range []string{"|", "\t", ";"} {
		if strings.Contains(line, sep) {
			return sep, true
		}
	}
	return "", false
}

// looksLikeDelimited 判断输入是否属于分隔符格式，并给出用得最多的分隔符。
//
// 先找单一分隔符的多数派；没有多数派时，再看「含任意候选分隔符的行」是否占多数 ——
// 同一批数据混用 `----` 与 `|` 的情况真实存在，只按单一分隔符判定会整批漏掉。
func looksLikeDelimited(input string) (string, bool) {
	lines := nonEmptyLines(input)
	if len(lines) == 0 {
		return "", false
	}
	best, bestCount := "", 0
	for _, sep := range delimiterCandidates {
		count := 0
		for _, l := range lines {
			if strings.Contains(l, sep) {
				count++
			}
		}
		if count > bestCount {
			best, bestCount = sep, count
		}
		if count > 0 && count*2 >= len(lines) {
			return sep, true
		}
	}
	hit := 0
	for _, l := range lines {
		if _, ok := pickLineSeparator(l); ok {
			hit++
		}
	}
	if hit > 0 && hit*2 >= len(lines) {
		if best == "" {
			best = "----"
		}
		return best, true
	}
	return "", false
}

// parseDelimited 按分隔符切列，再按位置推断字段。
//
// 分隔符**逐行**挑选，而不是整批共用一个：真实导出里同一批数据混用
// `----` 与 `|` 并不罕见，共用会让少数派那几行整行挤进一个字段
// （表现为「缺少邮箱」，或更糟 —— 整行被当成邮箱存进库）。
func parseDelimited(input string) *Result {
	res := &Result{Format: FormatDelimiter}
	for i, rawLine := range nonEmptyLines(input) {
		// 一行多列导出里，tab 之后是状态/凭证等元数据，不属于主体。
		line := stripMetaTail(rawLine)
		kind, ok := pickLineSeparator(line)
		var parts []string
		switch {
		case !ok:
			parts = []string{line}
		case kind == dashSeparator:
			parts = dashSplitRe.Split(line, -1)
		default:
			parts = strings.Split(line, kind)
		}
		for j := range parts {
			parts[j] = strings.TrimSpace(parts[j])
		}
		r := Row{Line: i + 1}
		assignByPosition(&r, parts)
		res.Rows = append(res.Rows, r)
	}
	return res
}
