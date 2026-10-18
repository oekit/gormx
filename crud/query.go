package crud

import (
	"fmt"
	"strings"

	"github.com/oekit/pkg/pagination"
)

// Condition 是一条 WHERE 条件的中性形态（SQL 片段 + 占位符参数）。
// Query string 由本包按操作符白名单生成（见 buildCondition），
// 值始终走占位符参数化；不识别的条件 fail-closed 渲染为恒假的 1=0。
type Condition struct {
	Query string
	Args  []any
}

// Preload 是一条关联预加载声明的中性形态。
type Preload struct {
	Assoc string
	Args  []any
}

// QuerySpec 是 Query 的中性物化形态，供存储后端实现（如 gormrepo）
// 翻译为具体查询语句。根包不依赖任何 ORM；后端按 Spec 的字段语义渲染：
//   - Conds 为 AND 条件、OrConds 为 OR 条件（均为有序）；
//   - ForUpdate 为悲观锁语义（仅事务内有意义，见 Query.ForUpdate 注释）；
//   - Page 为分页参数，nil 表示未分页。
type QuerySpec struct {
	Conds     []Condition
	OrConds   []Condition
	Orders    []string
	Preloads  []Preload
	Selects   []string
	Omits     []string
	GroupBy   string
	Page      *pagination.PageRequest
	ForUpdate bool
}

// Query 是查询条件构建器，支持链式调用。
// 存储后端无关：构建结果经 Spec() 物化为 QuerySpec 交由仓储实现消费。
type Query struct {
	conds     []Condition
	orConds   []Condition
	orders    []string
	preloads  []Preload
	selects   []string
	omits     []string
	groupBy   string
	page      *pagination.PageRequest
	forUpdate bool
}

// NewQuery 创建查询构建器。
func NewQuery() *Query {
	return &Query{}
}

// knownOps 是 Where/Or 允许的 SQL 操作符白名单。
// 操作符会直接拼入 SQL（值始终走占位符参数化），白名单外的操作符
// 一律 fail-closed（条件渲染为 1=0），防止任意 SQL 片段被拼接进 WHERE。
var knownOps = map[string]struct{}{
	"=": {}, "!=": {}, "<>": {},
	"<": {}, ">": {}, "<=": {}, ">=": {},
	"LIKE": {}, "ILIKE": {},
	"IN": {}, "NOT IN": {},
	"BETWEEN": {}, "NOT BETWEEN": {},
	"IS NULL": {}, "IS NOT NULL": {},
}

// buildCondition 将 (field, op, value) 解析为 SQL 片段与参数。
// 返回 ok=false 表示无法解析（操作符不在白名单，或 BETWEEN/NOT BETWEEN
// 值形状非法），调用方必须以 1=0 fail-closed 处理 —— 静默丢弃条件
// 可能让租户/权限过滤凭空消失（数据暴露面），绝不允许 fail-open。
//
// BETWEEN/NOT BETWEEN 支持三种值形状：
//
//	q.Where("age", "BETWEEN", [2]any{18, 65})
//	q.Where("age", "BETWEEN", []any{18, 65})
//	q.Where("age", "BETWEEN", 18, 65) // 两个连续变参
func buildCondition(field, op string, value []any) (query string, args []any, ok bool) {
	op = strings.ToUpper(strings.TrimSpace(op))
	if _, known := knownOps[op]; !known {
		return "", nil, false
	}

	single := func() any {
		if len(value) > 0 {
			return value[0]
		}
		return nil
	}

	switch op {
	case "IS NULL", "IS NOT NULL":
		return fmt.Sprintf("%s %s", field, op), nil, true
	case "IN", "NOT IN":
		if len(value) == 0 {
			return "", nil, false
		}
		if len(value) == 1 {
			// 单值（含 slice 参数）：GORM 会将 slice 参数自动展开为 (?, ?, ...)
			return fmt.Sprintf("%s %s (?)", field, op), value, true
		}
		// 多值变参（IN a, b, c）显式展开占位符——此前 single() 只保留第一个值，
		// 其余值被静默截断（IN (1,2,3) 退化成 IN (1)）
		placeholders := strings.TrimSuffix(strings.Repeat("?,", len(value)), ",")
		return fmt.Sprintf("%s %s (%s)", field, op, placeholders), value, true
	case "BETWEEN", "NOT BETWEEN":
		var lo, hi any
		switch {
		case len(value) == 1:
			switch vals := value[0].(type) {
			case [2]any:
				lo, hi = vals[0], vals[1]
			case []any:
				if len(vals) != 2 {
					return "", nil, false
				}
				lo, hi = vals[0], vals[1]
			default:
				return "", nil, false
			}
		case len(value) == 2:
			lo, hi = value[0], value[1]
		default:
			return "", nil, false
		}
		return fmt.Sprintf("%s %s ? AND ?", field, op), []any{lo, hi}, true
	case "LIKE", "ILIKE":
		return fmt.Sprintf("%s %s ?", field, op), []any{single()}, true
	default:
		// =, !=, <>, <, >, <=, >=
		return fmt.Sprintf("%s %s ?", field, op), []any{single()}, true
	}
}

// failClosedCondition 是无法识别条件的安全渲染结果：
// 恒为假，保证过滤器不被静默丢弃（fail-closed，绝不 fail-open）。
const failClosedCondition = "1=0"

// Where 添加 AND 条件。支持的操作符（白名单）:
//
//	q.Where("name", "=", "test")
//	q.Where("age", ">", 18)
//	q.Where("status", "IN", []int{1,2,3})
//	q.Where("name", "LIKE", "%test%")
//	q.Where("name", "ILIKE", "%test%")
//	q.Where("deleted_at", "IS NULL", nil)
//	q.Where("age", "BETWEEN", 18, 65)       // 两个连续变参
//	q.Where("age", "BETWEEN", []any{18, 65}) // len==2 切片
//
// 未知操作符、非法的 BETWEEN/NOT BETWEEN 值形状一律 fail-closed：
// 条件渲染为恒假的 1=0（绝不静默丢弃 —— 丢弃租户/权限过滤会造成数据暴露）。
func (q *Query) Where(field, op string, value ...any) *Query {
	query, args, ok := buildCondition(field, op, value)
	if !ok {
		q.conds = append(q.conds, Condition{Query: failClosedCondition})
		return q
	}
	q.conds = append(q.conds, Condition{Query: query, Args: args})
	return q
}

// Or 添加 OR 条件。
//
//	q.Where("status", "=", 1).Or("priority", ">", 5)
//	// => WHERE status = 1 OR priority > 5
//
// 与 Where 相同的操作符白名单与值形状约束；无法识别的 OR 分支同样
// fail-closed 渲染为 1=0（该分支不命中任何行），绝不静默丢弃。
func (q *Query) Or(field, op string, value ...any) *Query {
	query, args, ok := buildCondition(field, op, value)
	if !ok {
		q.orConds = append(q.orConds, Condition{Query: failClosedCondition})
		return q
	}
	q.orConds = append(q.orConds, Condition{Query: query, Args: args})
	return q
}

// Order 设置排序。
//
//	q.Order("created_at DESC")
//	q.Order("name ASC")
func (q *Query) Order(order string) *Query {
	q.orders = append(q.orders, order)
	return q
}

// Preload 预加载关联。
func (q *Query) Preload(assoc string, args ...any) *Query {
	q.preloads = append(q.preloads, Preload{Assoc: assoc, Args: args})
	return q
}

// Select 选择特定字段。
func (q *Query) Select(fields ...string) *Query {
	q.selects = append(q.selects, fields...)
	return q
}

// Omit 排除特定字段。
func (q *Query) Omit(fields ...string) *Query {
	q.omits = append(q.omits, fields...)
	return q
}

// Page 设置分页。
func (q *Query) Page(page, pageSize int) *Query {
	q.page = pagination.NewPageRequest(page, pageSize)
	return q
}

// ForUpdate 使用悲观锁（SELECT ... FOR UPDATE）。
//
// 注意：FOR UPDATE 仅在事务内有意义。在事务外调用时，行锁随语句执行
// 即取即放，等于没有锁（纯安慰剂）。请配合事务使用（见仓储实现的
// TxRepository.RunInTx）：
//
//	repo.RunInTx(ctx, func(txRepo Repository[T, ID]) error {
//	    entity, err := txRepo.GetByIDForUpdate(ctx, id)
//	    // ... 在同一事务内修改
//	})
func (q *Query) ForUpdate() *Query {
	q.forUpdate = true
	return q
}

// GroupBy 设置分组。
func (q *Query) GroupBy(field string) *Query {
	q.groupBy = field
	return q
}

// Spec 物化查询条件为后端无关的 QuerySpec。
// 返回的 Spec 与 Query 当前状态对应（切片共享底层，物化后继续链式
// 修改 Query 不影响已取得的 Spec）。
func (q *Query) Spec() QuerySpec {
	return QuerySpec{
		Conds:     append([]Condition(nil), q.conds...),
		OrConds:   append([]Condition(nil), q.orConds...),
		Orders:    append([]string(nil), q.orders...),
		Preloads:  append([]Preload(nil), q.preloads...),
		Selects:   append([]string(nil), q.selects...),
		Omits:     append([]string(nil), q.omits...),
		GroupBy:   q.groupBy,
		Page:      q.page,
		ForUpdate: q.forUpdate,
	}
}

// IsEmpty 是否没有任何条件。
func (q *Query) IsEmpty() bool {
	return len(q.conds) == 0 && len(q.orConds) == 0 && len(q.orders) == 0 && q.page == nil
}

// GetPage 获取分页参数，未设置时返回 nil。
func (q *Query) GetPage() *pagination.PageRequest {
	return q.page
}
