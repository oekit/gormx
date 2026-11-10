package gormx

import (
	"math"

	"github.com/oekit/pkg/pagination"
	"gorm.io/gorm"
	gclause "gorm.io/gorm/clause"
)

// PageResponse 分页查询的响应结果（pkg/pagination 统一信封的别名）。
// 字段为 Items（JSON "items"），与 crud、hertz/crudh 的契约统一为
// pkg/pagination.PageResponse。
type PageResponse[T any] = pagination.PageResponse[T]

// Paginate 执行分页查询，返回指定类型的分页结果。
// queryFn 只调用一次（GORM 的 *gorm.DB 是不可变的，每次方法调用返回新实例）。
// queryFn 可传 nil（等价于恒等 scope）。
//
// 若调用方既未在 db 上 Model() 也未在 queryFn 里 Table()/Model()，
// 自动用泛型参数 T 补 Model(new(T))——否则 Count 步会报
// "Table not set"，这是最常见的用法遗漏（13+ 处调用踩过）。
// 显式 Table() 的自定义行扫描（join/Select 到非表结构体）不受影响。
//
// req 不会被修改（#101）：归一化在内部副本上计算——handler 侧把包级共享的
// 默认 PageRequest 指针传入时，原地 Normalize 就是并发请求间的数据竞争
// （pkg/pagination 2026-09-06 已把 Offset/Limit 改为只读，此处对齐）。
//
// 排序稳定性（#95）：OFFSET/LIMIT 无 ORDER BY 时 SQL 语义上是**未定义顺序**
// ——同一查询两次执行的页内容都可能漂移，跨页边界行重复或丢失。检测到调用方
// 未提供 ORDER BY 时，Paginate 按模型主键自动注入确定性 tiebreaker
// （ORDER BY <主键> DESC）；显式 Table()/Select 到非表结构体的形态没有主键
// 可依，不注入——这类查询必须由调用方自带稳定排序。
func Paginate[T any](db *gorm.DB, req *pagination.PageRequest, queryFn func(db *gorm.DB) *gorm.DB) (*PageResponse[T], error) {
	// nil 请求按默认页兜底（handler 忘绑定分页参数时传 nil 是常见笔误，
	// 直接触发 Normalize 的 nil dereference panic）
	if req == nil {
		req = pagination.NewPageRequest(pagination.DefaultPage, pagination.DefaultPageSize)
	}
	// 只读归一化（#101）：不写 req。Offset()/Limit() 本身只读（内部走
	// normalized()），可直接调用；page/pageSize 副本仅用于响应回显与页数计算。
	page, pageSize := normalizePageParams(req.Page, req.PageSize)

	baseDB := db
	if queryFn != nil {
		baseDB = queryFn(db)
	}
	if st := baseDB.Statement; st != nil && st.Model == nil && st.Table == "" {
		baseDB = baseDB.Model(new(T))
	}

	total, err := countDroppingSelects(baseDB)
	if err != nil {
		return nil, err
	}

	findDB := baseDB.Offset(req.Offset()).Limit(req.Limit())
	if !hasOrderBy(findDB) {
		findDB = applyPrimaryKeyTiebreak(findDB)
	}

	var items []T
	if err := findDB.Find(&items).Error; err != nil {
		return nil, err
	}

	pages := 0
	if pageSize > 0 && total > 0 {
		pages = int(math.Ceil(float64(total) / float64(pageSize)))
	}

	return &PageResponse[T]{
		Items:    items,
		Total:    total,
		Page:     page,
		PageSize: pageSize,
		Pages:    pages,
	}, nil
}

// normalizePageParams 按包级口径（DefaultPage/DefaultPageSize/MaxPageSize，
// 与 pagination.PageRequest.Normalize 同一套规则）计算归一化副本，不修改入参。
func normalizePageParams(page, pageSize int) (int, int) {
	p := pagination.Policy{}
	return p.Normalize(page, pageSize)
}

// countDroppingSelects 执行 Count 并在**计数语句**上丢弃 Selects（#136）与
// LIMIT/OFFSET（#161）。
//
//   - Selects（#136）：GORM 的 Count 在 Statement.Selects 非空时生成
//     `COUNT(<列>)` 而非 count(*) ⇒ 目标列为 NULL 的行不计入 total（3 行含
//     1 行 name IS NULL ⇒ total 报 2），Total 与实际返回条数在同一响应里
//     自相矛盾、Pages 偏一（下一页永远取不到数据）。计数的语义是「行数」，
//     与查哪些列无关。
//   - LIMIT/OFFSET（#161）：baseDB 自带分页时（如 gormrepo.ApplyQuery 的
//     withPaging 形态）计数语句会带上 `LIMIT ? OFFSET ?`——聚合结果只有一行，
//     被 OFFSET 跳过 ⇒ page≥2 时 Total 恒为 0（Items 却有数据），
//     `SELECT count(*) FROM users LIMIT 10 OFFSET 10` 即现场。计数语义是
//     「全量行数」，与页码无关。
//
// 两个维度与 gormrepo.countStatement（forCount 丢弃 Selects/分页）同一口径
// （gormx 根包不能反向 import gormrepo，口径在此就地复刻，两处注释互指，
// 防再漂移）。
//
// 实现说明：baseDB 的 Statement 在 GORM 链式语义下与 find 路径共享（clone==0
// 的语句方法就地改写），经 Session 转手后再改字段会在克隆发生前污染共享
// 语句（issue 建议的 `Session(&Session{}); st.Selects = nil` 即踩此坑——
// find 路径的投影会被一并清掉）——故采用「置空 → Count → 还原」的窗口式
// 改写，与 gorm Count 内部对 SELECT/ORDER BY 子句的借用-还原模式同款；Count
// 返回（或 panic，经 defer）后语句恢复原状，find 路径的投影与分页不受影响。
func countDroppingSelects(db *gorm.DB) (int64, error) {
	var total int64
	st := db.Statement
	if st == nil {
		return total, db.Count(&total).Error
	}
	savedSelects := st.Selects
	savedLimit, hadLimit := st.Clauses[gclause.Limit{}.Name()]
	if len(savedSelects) > 0 {
		st.Selects = nil
	}
	if hadLimit {
		delete(st.Clauses, gclause.Limit{}.Name())
	}
	defer func() {
		st.Selects = savedSelects
		if hadLimit {
			st.Clauses[gclause.Limit{}.Name()] = savedLimit
		}
	}()
	return total, db.Count(&total).Error
}

// hasOrderBy 检测语句是否已带 ORDER BY 子句。
// 调用方自带排序时不注入 tiebreaker，尊重调用方口径（含非唯一列排序——
// 那是调用方显式选择的语义，库不再叠加）。
func hasOrderBy(db *gorm.DB) bool {
	st := db.Statement
	if st == nil {
		return false
	}
	c, ok := st.Clauses[gclause.OrderBy{}.Name()]
	if !ok {
		return false
	}
	if ob, isOrderBy := c.Expression.(gclause.OrderBy); isOrderBy {
		return len(ob.Columns) > 0 || ob.Expression != nil
	}
	return c.Expression != nil
}

// applyPrimaryKeyTiebreak 从模型 schema 解析主键列，注入 ORDER BY <主键> DESC
// 的确定性 tiebreaker（#95：同值行天然存在，非唯一排序键的分页边界不稳定，
// 主键是最后一道全序防线）。schema 不可解析（nil Model）或无主键（显式
// Table 的 join/扫描形态）时不注入，原语句返回。
func applyPrimaryKeyTiebreak(db *gorm.DB) *gorm.DB {
	st := db.Statement
	if st == nil || st.Model == nil {
		return db
	}
	parse := &gorm.Statement{DB: db}
	if err := parse.Parse(st.Model); err != nil {
		return db
	}
	field := parse.Schema.PrioritizedPrimaryField
	if field == nil || field.DBName == "" {
		return db
	}
	return db.Order(gclause.OrderByColumn{Column: gclause.Column{Name: field.DBName}, Desc: true})
}

// PaginateScope 返回一个 GORM Scope 函数，自动应用分页的 Offset 和 Limit。
func PaginateScope(req *pagination.PageRequest) func(db *gorm.DB) *gorm.DB {
	return func(db *gorm.DB) *gorm.DB {
		return db.Offset(req.Offset()).Limit(req.Limit())
	}
}

// Page 是「归一化页参数」的载体类型（#103）：把 Normalize + Offset + Limit
// 钉进一个类型，替代「两个裸 int 穿过多个 list 入口」的下传形态——裸 int
// 下传时 limit/offset 互换、每处重复 clamp 漏调，都没有类型防线。
//
// 与既有原语的关系：pagination.PageRequest 是 handler 入参绑定结构
// （query/form tag，值可非法、可被并发共享），Page 是归一化后的下传参数
// （构造即合法、不可变）；归一化口径复用 pagination.Policy 零值
// （DefaultPage/DefaultPageSize/MaxPageSize，与 PageRequest.Normalize 同一套
// 规则），不重复实现。
//
// 典型用法：
//
//	page := gormx.NormalizePage(req.Page, req.PageSize) // 1-based clamp + 上限
//	db.Scopes(page.Scope()).Find(&items)                // LIMIT/OFFSET 一体
//	page.Offset() / page.Limit()                        // 原生 SQL 等裸用出口
type Page struct {
	page     int
	pageSize int
}

// NormalizePage 归一化页参数并打包为 Page：page <= 0 回退默认页（1-based），
// pageSize <= 0 回退默认页长，超上限钳到 MaxPageSize（口径见 Page 文档）。
func NormalizePage(page, pageSize int) Page {
	p := pagination.Policy{}
	pg, ps := p.Normalize(page, pageSize)
	return Page{page: pg, pageSize: ps}
}

// Page 返回归一化后的页码（恒 >= 1）。
func (p Page) Page() int { return p.page }

// PageSize 返回归一化后的页长（恒 >= 1 且 <= MaxPageSize）。
func (p Page) PageSize() int { return p.pageSize }

// Limit 返回每页条数（原生 SQL 等场景的裸用出口）。
func (p Page) Limit() int { return p.pageSize }

// Offset 返回偏移量（原生 SQL 等场景的裸用出口）。
// 计算溢出时返回 0（回退到第一页）——溢出保护复用 pagination.Policy.Offset。
func (p Page) Offset() int {
	return pagination.Policy{}.Offset(p.page, p.pageSize)
}

// Request 转为 *pagination.PageRequest，下传给 Paginate、crud.Query 等
// 既有以 PageRequest 为入参的 API（值已是归一化后的）。
func (p Page) Request() *pagination.PageRequest {
	return pagination.NewPageRequest(p.page, p.pageSize)
}

// Scope 返回应用 Offset/Limit 的 GORM scope 函数（与 PaginateScope 同形态，
// 但参数已归一化、不可变，可安全复用与并发共享）。
func (p Page) Scope() func(db *gorm.DB) *gorm.DB {
	return func(db *gorm.DB) *gorm.DB {
		return db.Offset(p.Offset()).Limit(p.Limit())
	}
}
