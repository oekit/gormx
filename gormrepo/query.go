package gormrepo

import (
	"github.com/oekit/gormx/crud"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// compile-time:确保本包实现 crud 仓储接口与事务能力接口。
var (
	_ crud.Repository[int, int64]   = (*GormRepository[int, int64])(nil)
	_ crud.TxRepository[int, int64] = (*GormRepository[int, int64])(nil)
)

// ApplyQuery 将 crud.Query 以完整语句形态（分页 + 排序 + 悲观锁）
// 应用到 gorm.DB。
func ApplyQuery(db *gorm.DB, q *crud.Query) *gorm.DB {
	return applySpec(db, q.Spec(), applyOptions{withPaging: true})
}

// applyOptions 控制语句形态的内部选项。
type applyOptions struct {
	// withPaging 是否应用 Offset/Limit。
	withPaging bool
	// forCount 计数语句形态：跳过分页/排序/悲观锁。
	forCount bool
	// keepGroup 配合 forCount 使用：保留 GroupBy（供子查询对分组结果计数）。
	keepGroup bool
}

// countStatement 构建计数用语句：
//   - 跳过分页（Count 不能带 LIMIT/OFFSET，否则 OFFSET 跳过聚合行）
//   - 跳过排序（对计数无意义）
//   - 跳过悲观锁（FOR UPDATE 不允许出现在派生表中，且计数无需锁行）
//   - keepGroup=true 时保留 GroupBy（子查询计数形态）；
//     keepGroup=false 时剔除 GroupBy（GroupBy 会让 COUNT(*) 按组聚合，
//     GORM 只读第一行 → total 变成第一组的行数）
func countStatement(db *gorm.DB, q *crud.Query, keepGroup bool) *gorm.DB {
	return applySpec(db, q.Spec(), applyOptions{forCount: true, keepGroup: keepGroup})
}

// applySpec 按 opts 将 QuerySpec 翻译为 gorm.DB 语句。
// AND 条件逐条 Where（等价原 gormx.FilterBuilder.Apply 语义），
// OR 条件走 gorm Or()，值全部占位符参数化（片段由根包白名单生成）。
func applySpec(db *gorm.DB, spec crud.QuerySpec, opts applyOptions) *gorm.DB {
	// 应用 AND 条件
	for _, c := range spec.Conds {
		db = db.Where(c.Query, c.Args...)
	}

	// 应用 OR 条件
	for _, oc := range spec.OrConds {
		db = db.Or(oc.Query, oc.Args...)
	}

	// 应用预加载
	for _, p := range spec.Preloads {
		db = db.Preload(p.Assoc, p.Args...)
	}

	// 应用字段选择
	//
	// **计数语句不应用 Selects**：GORM 的 Count 在 Statement.Selects 非空时
	// 生成 `COUNT(<列>)` 而不是 `count(*)` ⇒
	//   - 该列为 NULL 的行被漏计（实测：3 行含 1 行 name IS NULL ⇒ total 报 2）；
	//   - 列名写错时直接 SQL 报错 "no such column"，计数整体失败。
	// 计数的语义是"行数"，与查哪些列无关 ⇒ 计数路径一律丢弃 Selects。
	if len(spec.Selects) > 0 && !opts.forCount {
		db = db.Select(spec.Selects)
	}

	// 应用字段排除
	if len(spec.Omits) > 0 {
		db = db.Omit(spec.Omits...)
	}

	// 应用分组
	if spec.GroupBy != "" && (!opts.forCount || opts.keepGroup) {
		db = db.Group(spec.GroupBy)
	}

	if !opts.forCount {
		// 应用排序
		for _, order := range spec.Orders {
			db = db.Order(order)
		}

		// 应用分页
		if opts.withPaging && spec.Page != nil {
			db = db.Offset(spec.Page.Offset()).Limit(spec.Page.Limit())
		}

		// 应用悲观锁
		if spec.ForUpdate {
			db = db.Clauses(clause.Locking{Strength: "UPDATE"})
		}
	}

	return db
}
