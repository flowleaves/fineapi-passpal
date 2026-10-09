package importer

import (
	"encoding/csv"
	"strings"
)

// looksLikeCSV 做一次轻量判断：多数非空行含逗号。
func looksLikeCSV(input string) bool {
	lines := nonEmptyLines(input)
	if len(lines) == 0 {
		return false
	}
	commaLines := 0
	for _, l := range lines {
		if strings.Contains(l, ",") {
			commaLines++
		}
	}
	return commaLines > 0 && commaLines*2 >= len(lines)
}

// parseCSV 用标准库 encoding/csv 处理引号、逗号与多行字段。
//
// 首行能解析出已知字段名时作为表头；否则按位置映射。
func parseCSV(input string) (*Result, error) {
	rdr := csv.NewReader(strings.NewReader(input))
	rdr.FieldsPerRecord = -1
	rdr.LazyQuotes = true
	rdr.TrimLeadingSpace = true

	records, err := rdr.ReadAll()
	if err != nil {
		return nil, err
	}
	res := &Result{Format: FormatCSV}
	if len(records) == 0 {
		return res, nil
	}

	// 判断首行是否为表头。
	header := records[0]
	headerFields := make([]string, len(header))
	recognized := 0
	hasEmailInHeader := false
	for i, h := range header {
		if f, ok := resolveField(h); ok {
			headerFields[i] = f
			recognized++
			if f == "email" {
				hasEmailInHeader = true
			}
		}
	}
	isHeader := recognized >= 2 && hasEmailInHeader && findEmail(strings.Join(header, ",")) == ""

	start := 0
	if isHeader {
		start = 1
	}

	for i := start; i < len(records); i++ {
		rec := records[i]
		r := Row{Line: i + 1}
		if isHeader {
			for j, v := range rec {
				if j >= len(headerFields) || headerFields[j] == "" {
					continue
				}
				assign(&r, headerFields[j], v)
			}
		} else {
			assignByPosition(&r, rec)
		}
		res.Rows = append(res.Rows, r)
	}
	return res, nil
}

// nonEmptyLines 返回去除空白后的非空行。
func nonEmptyLines(input string) []string {
	raw := strings.Split(strings.ReplaceAll(input, "\r\n", "\n"), "\n")
	out := make([]string, 0, len(raw))
	for _, l := range raw {
		l = strings.TrimSpace(l)
		if l != "" {
			out = append(out, l)
		}
	}
	return out
}
