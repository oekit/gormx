package gormx

import (
	"fmt"

	"github.com/oekit/gormx/internal/sqlident"
	"gorm.io/gorm"
	gclause "gorm.io/gorm/clause"
)

// SubqueryIn 给链上追加 `WHERE <column> IN (子查询)` 条件（方言安全，#81）。
//
// # 为什么必须是库级原语
//
// MySQL 不允许 DELETE/UPDATE 的 IN 子查询**直接引用目标表**（error 1093），
// 必须派生表包装：`... WHERE id IN (SELECT id FROM (SELECT ... ) AS t)`；
// 且 IN 子查询里的 LIMIT 只有包进派生表才合法。这条方言知识此前每个消费方
// 逐字复制（octopus 保留期清扫 4 个 repo 同构注释 + SQL），未来某方言要分支
// 得 N 处同步改——方言差异收编是本库的既定职责（IsUniqueViolation 同款）。
//
// # 方言策略
//
//   - mysql：自动派生表包装（`SELECT <column> FROM (<sub>) AS gormx_dt`），
//     1093 与 LIMIT 形态一次解决；
//   - SQLite / PostgreSQL 及其余方言：子查询直出（无 1093 限制）。
//
// # 用法（保留期清扫：删掉全部「已完成」行）
//
//	sub := db.Model(&SendLog{}).Select("id").Where("state = ?", "finished")
//	if err := gormx.DeleteBySubquery(db, &SendLog{}, "id", sub).Error; err != nil { ... }
//
// # 用法（批量 CAS 认领：一条语句完成多节点互斥认领，替代逐行 UPDATE）
//
//	pending := db.Model(&Outbox{}).Where("status = ?", 0).Select("id").Order("id").Limit(50)
//	err := gormx.SubqueryIn(db.Model(&Outbox{}).Where("status = ?", 0), "id", pending).
//	    Updates(map[string]any{"status": 1, "claimed_by": nodeID}).Error
//
// 约束：sub 必须是**未执行**的查询链（Model/Where/Select/Limit 等，不带
// finisher），且 SELECT 列表须包含/输出 column 命名的列（派生表外层按该
// 名取列）。column 必须是标识符白名单内的列名（#122，与 crud 包 Where/Or
// 字段名的 #100 纪律同款 fail-closed）——mysql 派生表路径经 Select(string)
// 原样渲染进 SELECT 列表，白名单外的值（逗号多列/括号表达式/子查询）会被
// 原样拼进 SQL。参数化不破坏：子查询的占位符与参数原序绑定。
func SubqueryIn(db *gorm.DB, column string, sub *gorm.DB) *gorm.DB {
	expr, err := subqueryInExpr(db, column, sub)
	if err != nil {
		_ = db.AddError(err)
		return db
	}
	return db.Where(expr)
}

// DeleteBySubquery 构造并执行 `DELETE FROM <model 表> WHERE <column> IN (子查询)`
// （语义与方言策略同 SubqueryIn，见其文档）。返回带 RowsAffected 的语句结果。
func DeleteBySubquery(db *gorm.DB, model any, column string, sub *gorm.DB) *gorm.DB {
	return SubqueryIn(db.Model(model), column, sub).Delete(model)
}

// subqueryColumnPattern 是 column 的标识符白名单（#122）：字母/下划线开头，
// 仅含字母、数字、下划线与点（表限定名 t.col）。单源在 internal/sqlident
// （#174，与 crud 包 Where/Or 字段名的 #100 纪律同一份）——column 名与
// field 名一样是「最可能从 config/query 参数流入 SQL 的值」，两个渲染面
// 都要兜住：外层 IN 走 clause.Column（Quote 安全），mysql 派生表内层走
// Select(string) 原样渲染（gorm 的 Select 不接受 clause.Column 值）⇒ 内层
// 是裸拼面，必须 fail-closed 拒白名单外的形态（逗号多列/括号/表达式/子查询）。
const subqueryColumnPattern = sqlident.PlainPattern

// subqueryInExpr 构造 `column IN (...)` 表达式：mysql 走派生表包装，
// 其余方言直出。列名经 gorm 方言 Quote（“ `id` “ / "id"），column 经
// clause.Column 走占位符渲染路径。
func subqueryInExpr(db *gorm.DB, column string, sub *gorm.DB) (gclause.Expression, error) {
	if column == "" {
		return nil, fmt.Errorf("gormx: SubqueryIn requires non-empty column")
	}
	if !sqlident.Match(column) {
		return nil, fmt.Errorf("gormx: SubqueryIn column %q is not a plain identifier "+
			"(expected %s, same discipline as crud Where/Or #100)", column, subqueryColumnPattern)
	}
	if sub == nil || sub.Statement == nil {
		return nil, fmt.Errorf("gormx: SubqueryIn requires a non-nil subquery chain")
	}
	inner := sub
	if wrapsDerivedTable(db.Name()) {
		// 派生表包装：外层只取 column 列。Table("(?) as gormx_dt", sub) 是
		// gorm 的子查询进 FROM 惯用形态，内层 SQL/参数由 gorm 自行渲染绑定
		// （Select 走字符串形态：clause.Column 值不被 Select 支持）。
		// column 已过标识符白名单（#122）⇒ Select(string) 原样渲染安全。
		inner = db.Session(&gorm.Session{NewDB: true}).
			Table("(?) as gormx_dt", sub).
			Select(column)
	}
	return gclause.Expr{SQL: "? IN (?)", Vars: []any{gclause.Column{Name: column}, inner}}, nil
}

// wrapsDerivedTable 报告该方言的 IN 子查询是否需要派生表包装。
// 分发独立成纯函数可单测（CI 无 MySQL 服务，方言形态靠纯函数 + DryRun 断言，
// 与 advisoryLockSQLs 同款验证面）。
func wrapsDerivedTable(dialect string) bool {
	return dialect == "mysql"
}
