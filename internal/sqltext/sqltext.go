// Package sqltext 是「引号/括号感知的 SQL 文本切分原语」单源（#175）。
//
// 此前同语义两实现分叉：根包 redact（带逃逸，#138 正典——双写引号与反斜杠
// 转义均识别）与 migrate（裸引号状态机，无逃逸处理）——同一健壮性需求两个
// 等级。本包以 redact 版为正典下沉单源，migrate.splitTopLevelClauses 改调
// 本包后顺带获得反斜杠逃逸能力（对含转义引号的 DDL 是修正而非回归，见
// migrate CHANGELOG v0.11.14 节）。
//
// 三种引号（'、"、`）内的分隔符/括号不参与顶层结构判定；引号逃逸口径：
//   - 双写引号（同一引号字符连写两次）＝字面引号，仍在引号内（SQL 标准
//     与 MySQL 方言）；
//   - 反斜杠转义（\' / \"，MySQL 方言）——下一字节原样跳过；反引号为
//     标识符引号，不适用反斜杠转义。
package sqltext

import "strings"

// IsQuote 报告 c 是否 SQL 引号（单引号/双引号/反引号）。
func IsQuote(c byte) bool {
	return c == '\'' || c == '"' || c == '`'
}

// SkipQuoted 返回从 open（引号开字符）出发的引号区段末字节下标：闭引号
// 位置；未闭合则吃到底（返回 len(s)-1）。逃逸口径见包文档。
func SkipQuoted(s string, open int) int {
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

// MatchParen 返回从 open 位置（'('）出发的配对闭括号后一位；引号内的
// 括号/分隔符跳过（含逃逸，见 SkipQuoted 与包文档）。未找到配对返回 -1
// （调用方按失败闭合处置）。
func MatchParen(s string, open int) int {
	depth := 0
	for i := open; i < len(s); i++ {
		c := s[i]
		if IsQuote(c) {
			i = SkipQuoted(s, i)
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

// SplitTopLevel 按分隔符切分，跳过引号内（含逃逸，见 SkipQuoted 与包文档）
// 与嵌套括号内的分隔符。段两侧空白裁剪；引号未闭合时剩余部分并入当前段
// （失败闭合方向：不会把未配对引号误当新段边界）。
//
// 空段口径：**中段**空段保留为 ""（消费方按位置对齐的形态——如 INSERT 元组
// 列序——依赖下标稳定），**尾段**空段丢弃。需要全丢弃空段的消费方（如
// ALTER 子句切分）在调用侧过滤。
func SplitTopLevel(s string, sep byte) []string {
	var parts []string
	depth := 0
	start := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		if IsQuote(c) {
			i = SkipQuoted(s, i)
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
