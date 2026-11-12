// Package scoper 提供行级数据可见性规则的单源插件（#102）：同一份规则定义
// 同时驱动 SQL WHERE 面（GORM scope）与内存判定面（CheckScope 形态），
// 消除「SQL 侧手搓 WHERE 变体 + 服务层 if 同规则两处维护」的双轨漂移面
// ——规则改动要两处同步、SQL 侧列名字符串拼接无类型保护、拼错编译期不可发现。
//
// 规则形态覆盖消费方实证的「admin 全量 / 其余仅本人 + 内置行可见」矩阵：
//
//	rule := scoper.NewRule[Task]("uid", func(t Task) int64 { return t.UID },
//	    scoper.WithBuiltinRows("is_builtin", true, func(t Task) bool { return t.Builtin }))
//
//	// SQL 面：与既有仓储形态共存（Scopes 非侵入）
//	db.Scopes(rule.Scope(scoper.Subject{UID: uid, Admin: isAdmin})).Find(&items)
//
//	// 内存面：同一份规则求值（服务层 CheckScope 的单源替身）
//	if !rule.Allows(item, scoper.Subject{UID: uid, Admin: isAdmin}) { ... }
//
// # 与两轨现状的关系
//
// 消费方此前的三变体（ApplyScope / ApplyScopeOrBuiltin / ApplyScopeWithSystem）
// 在这里是同一 Rule 的两种配置：不配 WithBuiltinRows = 仅本人；配上 = 本人
// OR 内置行。admin 旁路在 Subject 上显式声明（role→admin 的解释属调用方）。
//
// # fail-closed 契约
//
// 规则未配置完整（ownerColumn 空 / ownerOf nil；配置了 WithBuiltinRows 时
// column 空 / match nil，#195）时：SQL 面注入恒假 1=0、内存面恒 false——
// 可见性规则缺失时宁可全不可见，绝不无过滤放行（与 crud 包 Query 的
// fail-closed 同一纪律）。
package scoper

import (
	"gorm.io/gorm"
	gclause "gorm.io/gorm/clause"
)

// Subject 是一次可见性判定的主体。
type Subject struct {
	// UID 行所有者标识（与 Rule 的 OwnerColumn 同域）。
	UID int64
	// Admin 管理员旁路：true 时全量可见（SQL 面恒等 scope / 内存面恒 true）。
	Admin bool
}

// Rule[T] 是行级可见性规则的单源定义：admin 全量 / 其余仅本人，
// 可选内置行（如系统预置数据）对所有人可见。不可变，构造后可并发共享。
type Rule[T any] struct {
	ownerColumn string
	ownerOf     func(T) int64
	builtin     *builtinRule[T]
}

// builtinRule 是内置行可见性声明的两面：column/value 驱动 SQL 面
// （WHERE <column> = <value>），match 驱动内存面。两面在同一个配置点
// 相邻声明（这正是「单源」要消灭的双轨漂移面的收口处）——match 必须与
// column=value 同义，Rule 的测试矩阵（TestRule_Matrix 两面一致性）固化
// 这一契约的验证形态。
type builtinRule[T any] struct {
	column string
	value  any
	match  func(T) bool
}

// RuleOption 配置 Rule 的行为（函数选项模式；不裸用 Option 之名，
// 避免多包 co-import 冲突）。
type RuleOption[T any] func(*Rule[T])

// NewRule 创建行级可见性规则。ownerColumn 是行所有者的**数据库列名**
// （SQL WHERE 侧直接使用，禁止拼入用户输入）；ownerOf 从实体取所有者标识
// （内存面），两者必须指向同一语义列。
func NewRule[T any](ownerColumn string, ownerOf func(T) int64, opts ...RuleOption[T]) *Rule[T] {
	r := &Rule[T]{ownerColumn: ownerColumn, ownerOf: ownerOf}
	for _, opt := range opts {
		if opt != nil {
			opt(r)
		}
	}
	return r
}

// WithBuiltinRows 声明内置行可见：非 admin 主体除本人行外还可见
// `<column> = <value>` 的行（SQL 面 owner = ? OR column = value；
// 内存面 ownerOf(row) == uid 或 match(row)）。column 禁止拼入用户输入。
//
// column 为空或 match 为 nil 属非法配置：规则**整体**走 fail-closed
// （SQL 面恒假 1=0、内存面恒 false，#195）——此前不在完整性判定里，
// match nil 在 Allows 内存面 nil func call 直接 panic、column 空在 Scope
// SQL 面拼出非法 SQL（500），都不是声明的 fail-closed 形态。
func WithBuiltinRows[T any](column string, value any, match func(T) bool) RuleOption[T] {
	return func(r *Rule[T]) {
		r.builtin = &builtinRule[T]{column: column, value: value, match: match}
	}
}

// valid 报告规则是否配置完整（fail-closed 判定用）：owner 侧与 builtin 侧
// 任一缺失即不完整（#195——builtin 误配此前不在判定里，两面表现分别为
// 内存面 panic 与 SQL 面 500，与包文档声明的「恒假 1=0 / 恒 false」不符）。
func (r *Rule[T]) valid() bool {
	if r == nil || r.ownerColumn == "" || r.ownerOf == nil {
		return false
	}
	if r.builtin != nil && (r.builtin.column == "" || r.builtin.match == nil) {
		return false
	}
	return true
}

// Scope 返回 SQL 面的 GORM scope 函数：
//
//   - Admin：恒等 scope（全量，不过滤）；
//   - 规则缺失：注入恒假 1=0（fail-closed，绝不无过滤放行）；
//   - 其余：WHERE owner = uid（配置了内置行时为 owner = uid OR col = val）。
//
// 返回的 scope 无共享可变状态，可复用、可并发。
func (r *Rule[T]) Scope(sub Subject) func(db *gorm.DB) *gorm.DB {
	return func(db *gorm.DB) *gorm.DB {
		if sub.Admin {
			return db
		}
		if !r.valid() {
			return db.Where(gclause.Expr{SQL: "1=0"})
		}
		ownerEq := gclause.Eq{Column: gclause.Column{Name: r.ownerColumn}, Value: sub.UID}
		if r.builtin != nil {
			return db.Where(gclause.Or(
				ownerEq,
				gclause.Eq{Column: gclause.Column{Name: r.builtin.column}, Value: r.builtin.value},
			))
		}
		return db.Where(ownerEq)
	}
}

// Allows 是内存面判定：与 Scope 完全同一规则求值。
//
//	Admin ⇒ true；ownerOf(row) == sub.UID ⇒ true；
//	配置了内置行且 match(row) ⇒ true；其余 false。
//
// 规则缺失时恒 false（fail-closed，与 Scope 的 1=0 对偶）。
func (r *Rule[T]) Allows(row T, sub Subject) bool {
	if sub.Admin {
		return true
	}
	if !r.valid() {
		return false
	}
	if r.ownerOf(row) == sub.UID {
		return true
	}
	return r.builtin != nil && r.builtin.match(row)
}
