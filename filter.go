package gormx

import "gorm.io/gorm"

// FilterBuilder 构建动态 SQL WHERE 条件。
// 适用于查询参数可选的场景，避免大量 if-else 拼接。
//
// 用法:
//
//	fb := gormx.NewFilterBuilder()
//	fb.Add(opts.Provider != "", "provider = ?", opts.Provider)
//	fb.Add(opts.Repo != "", "repo ILIKE ?", "%"+opts.Repo+"%")
//	fb.Add(!opts.From.IsZero(), "created_at >= ?", opts.From)
//	db = fb.Apply(db)
type FilterBuilder struct {
	clauses []clause
}

type clause struct {
	sql  string
	args []any
}

// NewFilterBuilder 创建过滤构建器。
func NewFilterBuilder() *FilterBuilder {
	return &FilterBuilder{}
}

// Add 条件为 true 时追加 WHERE 子句。
//
// 片段进入 AND 链时自动包一层括号（参数绑定不变）：片段内部含 OR 时不会因
// SQL 优先级把整条 AND 链击穿成 (... AND a) OR b —— 未加括号的 OR 右侧会
// 绕过之前所有 AND 过滤，静默返回不该返回的行（fail-open 漏数据）。
// 自动括号后忘写手括号不再是错误方向的安全缺口。
func (fb *FilterBuilder) Add(ok bool, sql string, args ...any) *FilterBuilder {
	if ok {
		fb.clauses = append(fb.clauses, clause{sql: "(" + sql + ")", args: args})
	}
	return fb
}

// Apply 将构建的条件应用到 GORM 查询链。
// 每个条件之间用 AND 连接。
func (fb *FilterBuilder) Apply(db *gorm.DB) *gorm.DB {
	for _, c := range fb.clauses {
		db = db.Where(c.sql, c.args...)
	}
	return db
}

// IsEmpty 是否没有任何条件。
func (fb *FilterBuilder) IsEmpty() bool {
	return len(fb.clauses) == 0
}

// Clauses 返回合并后的子句字符串和参数（用于 raw SQL 场景）。
// 每个片段自带括号（见 Add），合并结果形如 "(a) AND (b OR c)"。
func (fb *FilterBuilder) Clauses() (string, []any) {
	if len(fb.clauses) == 0 {
		return "", nil
	}
	var combined string
	var args []any
	for i, c := range fb.clauses {
		if i > 0 {
			combined += " AND "
		}
		combined += c.sql
		args = append(args, c.args...)
	}
	return combined, args
}
