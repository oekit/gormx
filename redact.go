package gormx

import (
	"regexp"
	"strings"
)

// redactor 是 SQL 日志的列级脱敏器（#106）：按列名把绑定值替换为 ***，
// 语句结构保留（哪列被写、什么形状的条件仍可读，排障不失真）。
//
// # 覆盖面（best-effort，文档明示）
//
// GORM 的 Trace 拿到的是 Explain 后的完整语句字符串（值已内联），列级脱敏
// 只能在字符串面上做。覆盖三类泄漏面：
//
//   - 赋值形态：SET `password` = 'x' / WHERE token = 'y'（UPDATE/SELECT/WHERE）；
//   - INSERT 元组：INSERT INTO t (a, password) VALUES (1, 'x')（含多行元组），
//     按列位置对应替换；
//   - 引号内的逗号/括号正确处理（值本身含分隔符不误切）：SQL 引号逃逸按
//     双写（同一引号字符连写两次）与反斜杠转义（\' / \"，MySQL 方言）识别，
//     值本身含引号不再把闭引号误当开引号（#138）。
//
// 失败闭合（#138）：元组的括号配对无法解析时（语句截断、引号逃逸方言
// 不可判定），存在敏感列的语句不原文放行剩余部分——整段替换为 ***，
// 宁可语句结构受损也不泄漏敏感值。
//
// 未覆盖（文档明示）：SELECT 列表投影（SELECT password FROM ...——值在结果
// 集不在语句里）。替换只发生在「列名命中 + 值边界明确」的位置；边界无法
// 判定时按上一条失败闭合。
//
// 并发安全：构造后只读（regexp 并发安全），可被多个 Trace 并发使用。
type redactor struct {
	columns    map[string]struct{}
	assignment *regexp.Regexp
}

// insertHeaderRe 匹配 INSERT 头部到 VALUES 关键字：捕获组 1 = 列清单。
var insertHeaderRe = regexp.MustCompile(`(?is)INSERT\s+(?:IGNORE\s+)?INTO\s+[^\s(]+\s*\(([^)]*)\)\s*VALUES\s*`)

// newRedactor 构造脱敏器；列名为空集时返回 nil（等价于不脱敏，调用方零开销）。
func newRedactor(cols []string) *redactor {
	normalized := make(map[string]struct{}, len(cols))
	var names []string
	for _, c := range cols {
		n := normalizeColumnName(c)
		if n == "" {
			continue
		}
		if _, dup := normalized[n]; dup {
			continue
		}
		normalized[n] = struct{}{}
		names = append(names, regexp.QuoteMeta(n))
	}
	if len(names) == 0 {
		return nil
	}
	// 赋值形态：可选引号（`col` / "col" / [col]）+ = + 值
	// （引号串——含 '' 双写与 \' 反斜杠逃逸（#138），或到 , ) 空白为止的裸值）。
	// 保留左侧（列名与等号）便于排障。引号分支配不平（截断/方言不可判定）
	// 时落到裸值分支，到 , ) 空白为止整段替换——失败闭合不残留尾巴。
	alt := strings.Join(names, "|")
	re, err := regexp.Compile(`(?i)((?:` + "`" + `|"|\[)?(?:` + alt + `)(?:` + "`" + `|"|\])?\s*=\s*)('(?:[^'\\]|\\.|'')*'|"(?:[^"\\]|\\.|"")*"|[^,)\s]+)`)
	if err != nil {
		return nil
	}
	return &redactor{columns: normalized, assignment: re}
}

// normalizeColumnName 规范化列名：去引号（`/"/[]）、小写、去空白。
func normalizeColumnName(c string) string {
	c = strings.TrimSpace(strings.ToLower(c))
	c = strings.Trim(c, "`\"[]")
	return strings.TrimSpace(c)
}

// redact 对完整 SQL 语句做列级脱敏。
func (r *redactor) redact(sql string) string {
	if r == nil {
		return sql
	}
	sql = r.redactAssignments(sql)
	sql = r.redactInserts(sql)
	return sql
}

// redactAssignments 替换赋值/比较形态的敏感列值（SET/WHERE）。
func (r *redactor) redactAssignments(sql string) string {
	return r.assignment.ReplaceAllString(sql, "${1}***")
}

// redactInserts 替换 INSERT 元组中敏感列位置的值（含多行元组）。
func (r *redactor) redactInserts(sql string) string {
	pos := 0
	var out strings.Builder
	for pos < len(sql) {
		loc := insertHeaderRe.FindStringSubmatchIndex(sql[pos:])
		if loc == nil {
			out.WriteString(sql[pos:])
			return out.String()
		}
		headerEnd := pos + loc[1]
		cols := splitTopLevel(sql[pos+loc[2]:pos+loc[3]], ',')
		out.WriteString(sql[pos:headerEnd])
		pos = headerEnd

		sensitive := make(map[int]bool, len(cols))
		for i, c := range cols {
			if _, hit := r.columns[normalizeColumnName(c)]; hit {
				sensitive[i] = true
			}
		}
		if len(sensitive) == 0 {
			continue // 本语句无敏感列，元组原样（交给下一轮匹配继续扫描）
		}

		// 逐元组解析替换：( ... ) (, ... )* —— 值内引号/逗号正确跳过
		for {
			for pos < len(sql) && isSQLSpace(sql[pos]) {
				pos++
			}
			if pos >= len(sql) || sql[pos] != '(' {
				break
			}
			end := matchParen(sql, pos)
			if end < 0 {
				// 失败闭合（#138）：括号配对无法解析（语句截断、引号逃逸
				// 方言不可判定）时，本语句存在敏感列，剩余字节不原文放行
				// ——整段替换为 ***，宁可语句结构受损不泄漏敏感值。
				out.WriteString("***")
				return out.String()
			}
			values := splitTopLevel(sql[pos+1:end-1], ',')
			for i := range values {
				if sensitive[i] {
					values[i] = "***"
				}
			}
			out.WriteString("(" + strings.Join(values, ",") + ")")
			pos = end
			// 逗号后还有下一个元组则继续
			next := pos
			for next < len(sql) && isSQLSpace(sql[next]) {
				next++
			}
			if next < len(sql) && sql[next] == ',' {
				out.WriteString(", ")
				pos = next + 1
				continue
			}
			break
		}
	}
	return out.String()
}

// matchParen 返回从 open 位置（'('）出发的配对闭括号后一位；引号内的
// 括号/分隔符跳过（含 SQL 引号逃逸，见 skipQuoted）。未找到配对返回 -1
// （调用方失败闭合）。
func matchParen(s string, open int) int {
	depth := 0
	for i := open; i < len(s); i++ {
		c := s[i]
		if isSQLQuote(c) {
			i = skipQuoted(s, i)
			continue
		}
		switch c {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return i + 1
			}
		}
	}
	return -1
}

// splitTopLevel 按分隔符切分，跳过引号内（含逃逸，见 skipQuoted）与
// 嵌套括号内的分隔符。引号未闭合时剩余部分并入当前段（失败闭合方向：
// 不会把未配对引号误当新值边界）。
func splitTopLevel(s string, sep byte) []string {
	var parts []string
	depth := 0
	start := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		if isSQLQuote(c) {
			i = skipQuoted(s, i)
			continue
		}
		switch c {
		case '(':
			depth++
		case ')':
			depth--
		case sep:
			if depth == 0 {
				parts = append(parts, strings.TrimSpace(s[start:i]))
				start = i + 1
			}
		}
	}
	if strings.TrimSpace(s[start:]) != "" {
		parts = append(parts, strings.TrimSpace(s[start:]))
	}
	return parts
}

func isSQLQuote(c byte) bool {
	return c == '\'' || c == '"' || c == '`'
}

// skipQuoted 返回从 open（引号开字符）出发的引号区段末字节下标：闭引号
// 位置；未闭合则吃到底。SQL 引号逃逸（#138）：
//   - 双写引号（同一引号字符连写两次）＝字面引号，仍在引号内；
//   - 反斜杠转义（\' / \"，MySQL 方言）——下一字节（含转义引号）原样跳过；
//     反引号为标识符引号，不适用反斜杠转义。
func skipQuoted(s string, open int) int {
	q := s[open]
	allowBackslash := q != '`'
	for i := open + 1; i < len(s); i++ {
		c := s[i]
		switch {
		case allowBackslash && c == '\\':
			i++ // 转义下一字节（若越界则循环自然终止）
		case c == q:
			if i+1 < len(s) && s[i+1] == q {
				i++ // 双写引号＝字面引号，仍在引号内
				continue
			}
			return i
		}
	}
	return len(s) - 1
}

func isSQLSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r'
}
