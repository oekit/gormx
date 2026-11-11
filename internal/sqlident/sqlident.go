// Package sqlident 是 gormx 矩阵内「SQL 标识符白名单」的单源（#174）。
//
// 根包（subquery 的 SubqueryIn column，#122）与 crud 包（Where/Or 字段名的
// #100 纪律）此前各自维护一份同款正则，一致性靠注释互指维持。根包是 ORM
// 强绑定包、crud 是 ORM 无关包，双向都不宜 import 对方——下沉 internal
// 单源后，漂移即不可能。
//
// 白名单口径（fail-closed）：字母/下划线开头，仅含字母、数字、下划线与点
// （表限定名 t.col）。字段/列名与操作符一样会直接拼入 SQL 片段（GORM Order/
// Select 即裸拼），白名单外的形态（逗号多列/括号表达式/子查询/引号）必须
// 被拒绝，绝不放行——「最可能从 config/query 参数流入 SQL 的值」都要过这道门。
package sqlident

import "regexp"

// PlainBody 是裸标识符正则去掉 ^/$ 锚点后的主体，供复合白名单拼接复用
// （如 crud 排序子句「标识符 [ASC|DESC]」、投影「* / 标识符 / t.*」）。
const PlainBody = `[A-Za-z_][A-Za-z0-9_.]*`

// PlainPattern 是裸标识符（可表限定）白名单正则全文（带锚点）。导出常量供
// 错误文案等场景引用（此前错误信息里硬编码同一正则文本，与白名单本体漂移
// 无信号）。
const PlainPattern = "^" + PlainBody + "$"

var plainRe = regexp.MustCompile(PlainPattern)

// Match 报告 s 是否为白名单内的裸标识符（字母/下划线开头，仅含字母、数字、
// 下划线与点）。
func Match(s string) bool {
	return plainRe.MatchString(s)
}
