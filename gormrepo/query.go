package gormrepo

import (
	"strings"

	"github.com/oekit/gormx"
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
//
// 排序稳定性（#269）：Page != nil 且未显式 Order 时，若 db 已绑定模型
// （db.Model(...)，如 Repository.List 内部所做），按模型主键注入
// ORDER BY <主键> DESC 的确定性 tiebreaker——与 gormx.Paginate 的 #95
// 同一口径。db 未绑定模型、或主键不可解析（显式 Table 的 join/扫描形态）
// 时**保持原语句不注入**：这类查询必须由调用方自带稳定排序。
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

// rewriteILikeMySQL 把 ILIKE 谓词改写为 MySQL 等价形态（#234）：
// `<field> ILIKE ?` → `LOWER(<field>) LIKE LOWER(?)`。大小写不敏感语义
// 等价；值仍走占位符参数化（LOWER 包住占位符，绑定值原样传入）。片段由
// crud 白名单生成、形态固定为单谓词 `<field> ILIKE ?`，无 ILIKE 时原样返回。
func rewriteILikeMySQL(query string) string {
	i := strings.Index(query, " ILIKE ")
	if i < 0 {
		return query
	}
	return "LOWER(" + strings.TrimSpace(query[:i]) + ") LIKE LOWER(?)"
}

// applySpec 按 opts 将 QuerySpec 翻译为 gorm.DB 语句。
// AND 条件逐条 Where（等价原 gormx.FilterBuilder.Apply 语义），
// OR 条件走 gorm Or()，值全部占位符参数化（片段由根包白名单生成）。
func applySpec(db *gorm.DB, spec crud.QuerySpec, opts applyOptions) *gorm.DB {
	// #234：ILIKE 方言分派——crud.Query 是存储后端无关的构建器，ILIKE 在
	// 白名单里按中性片段渲染；PostgreSQL/SQLite 原生支持，MySQL 没有
	// ILIKE（裸拼直接语法错）。此处是唯一知道方言的消费点：MySQL 下改写为
	// LOWER(field) LIKE LOWER(?)（语义等价的大小写不敏感匹配，占位符参数
	// 原样保留）。SQLite/PG 及未知方言原样透传。
	mysql := db != nil && db.Dialector != nil && db.Name() == "mysql"
	applyConds := func(conds []crud.Condition, or bool) {
		for _, c := range conds {
			q := c.Query
			if mysql {
				q = rewriteILikeMySQL(q)
			}
			if or {
				db = db.Or(q, c.Args...)
			} else {
				db = db.Where(q, c.Args...)
			}
		}
	}
	applyConds(spec.Conds, false)
	applyConds(spec.OrConds, true)

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
			// #269：未显式排序时 OFFSET/LIMIT 无全序保证（计划变化/并发写会
			// 让跨页行重复或丢失）。对齐 gormx.Paginate 的 #95 口径注入主键
			// DESC tiebreaker；schema 不可解析时保持现状（见 helper 注释）。
			if len(spec.Orders) == 0 {
				db = applyPagingTiebreak(db)
			}
		}

		// 应用悲观锁
		if spec.ForUpdate {
			db = db.Clauses(clause.Locking{Strength: "UPDATE"})
		}
	}

	return db
}

// applyPagingTiebreak 在分页语句未显式排序时注入 ORDER BY <主键> DESC 的
// 确定性 tiebreaker（#269，对齐 gormx.Paginate 的 #95 口径）：OFFSET/LIMIT
// 没有 ORDER BY 时 SQL 语义上是**未定义顺序**，同一查询两次执行的页内容
// 都可能漂移，跨页边界行重复或丢失（对账/导出静默不一致）。
//
// 模型取自 Statement.Model（其次 Dest）。调用方未绑定模型、或模型无主键
// （显式 Table 的 join/扫描形态）时**保持原语句不注入**——这类查询必须由
// 调用方自带稳定排序，与 pagination.go 的文档化契约一致。解析失败是
// 「排序增强不可用」而非查询失败，故显式忽略 error 并原样返回（此处是
// 语义分支而非 `_` 丢弃；上抛会把增强不可用放大成整个查询失败）。
func applyPagingTiebreak(db *gorm.DB) *gorm.DB {
	st := db.Statement
	if st == nil {
		return db
	}
	model := st.Model
	if model == nil {
		model = st.Dest
	}
	if model == nil {
		return db
	}
	col, err := gormx.PrimaryKeyColumn(db, model)
	if err != nil {
		return db
	}
	return db.Order(clause.OrderByColumn{Column: clause.Column{Name: col}, Desc: true})
}
