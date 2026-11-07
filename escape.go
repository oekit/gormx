package gormx

import "strings"

// EscapeLike 转义 LIKE 通配符，参数化 SQL 只防注入不防**通配符注入**——
// 用户输入的 % 会把「包含匹配」变成全匹配、_ 变单字符通配（#114）。
//
// 转义顺序即优先级：先 `\` 后 `%` 再 `_`——顺序错会让 `%` 被二次转义。
// 转义符统一用 `\`：MySQL/PostgreSQL 默认即认；**SQLite 的 LIKE 默认没有
// 转义字符**，查询须显式带 ESCAPE 子句：
//
//	db.Where("name LIKE ? ESCAPE '\\'", gormx.LikeContains(kw))
func EscapeLike(s string) string {
	r := strings.ReplaceAll(s, `\`, `\\`)
	r = strings.ReplaceAll(r, `%`, `\%`)
	r = strings.ReplaceAll(r, `_`, `\_`)
	return r
}

// LikeContains 转义后包裹为 %s%（包含匹配），配合 `col LIKE ?` 使用。
func LikeContains(s string) string {
	return "%" + EscapeLike(s) + "%"
}

// LikePrefix 转义后包裹为 s%（前缀匹配）。
func LikePrefix(s string) string {
	return EscapeLike(s) + "%"
}

// LikeSuffix 转义后包裹为 %s（后缀匹配）。
func LikeSuffix(s string) string {
	return "%" + EscapeLike(s)
}
