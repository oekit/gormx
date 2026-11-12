package crud

import (
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"strings"

	"github.com/oekit/gormx/internal/sqlident"
	"github.com/oekit/pkg/pagination"
)

// Condition 是一条 WHERE 条件的中性形态（SQL 片段 + 占位符参数）。
// Query string 由本包按「字段名白名单 + 操作符白名单」生成（见 buildCondition），
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
//   - Conds 为 AND 条件、OrConds 为 OR 条件（均为有序）；分组（WhereGroup/
//     OrGroup）在物化前已折叠为单个带括号条件，后端无需感知分组结构；
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
	// strict 是 #135 的严格模式标记：fail-closed 渲染发生时立即 panic
	// （把「静默失效」变「当场爆炸」），见 Strict。
	strict bool
	// failClosedErrs 累计链上每一次 fail-closed 渲染（#135 信号面），
	// 经 Err() 读出；无论是否开启 strict 都累计。
	failClosedErrs []error
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
	"FIND_IN_SET": {},
	"IN":          {}, "NOT IN": {},
	"BETWEEN": {}, "NOT BETWEEN": {},
	"IS NULL": {}, "IS NOT NULL": {},
}

// isValidField 报告字段名是否在标识符白名单内（#100）。
// 白名单口径：字母/下划线开头，仅含字母、数字、下划线与点（表限定名 t.col /
// json 路径 col->>key 之外的常规形态）。field 与 op 一样会直接拼入 SQL
// 片段——把前端可控的「排序/筛选字段名」透传给 Where 就是经典 SQL 注入，
// fail-closed 必须同时覆盖 op 与 field 两个面。
// 单源在 internal/sqlident（#174，与根包 SubqueryIn column 同一份）。
func isValidField(field string) bool {
	return sqlident.Match(field)
}

// validOrderClausePattern 是排序子句的白名单（#154）：字段名（可表限定）
// + 可选方向关键字 ASC/DESC。Order 的字符串整串直通后端 raw 拼接面
// （GORM Order 即裸拼，官方文档明示需自行防注入）——前端可控的「排序
// 字段名」透传进来就是 SQL 注入，与 Where 的 field 同一面。
// 标识符主体拼接自 sqlident.PlainBody（#174 单源）。
var validOrderClausePattern = regexp.MustCompile(
	`^(?:` + sqlident.PlainBody + `)(?:\s+(?i:ASC|DESC))?$`)

// validSelectPattern 是 Select 投影的白名单（#154）：裸标识符（可表限定）
// 与通配投影（* / t.*）。聚合表达式（COUNT(*)、子查询等）是 raw SQL 片段，
// 不在白名单内——投影名透传即 SQL 注入面。
// 标识符分支拼接自 sqlident.PlainBody（#174 单源）。
var validSelectPattern = regexp.MustCompile(
	`^(?:\*|` + sqlident.PlainBody + `|[A-Za-z_][A-Za-z0-9_]*\.\*)$`)

// isValidOrderList 报告排序串的全部子句是否都在排序白名单内（可逗号分隔
// 多段："a DESC, b ASC"）。
func isValidOrderList(order string) bool {
	if strings.TrimSpace(order) == "" {
		return false
	}
	for _, part := range strings.Split(order, ",") {
		part = strings.TrimSpace(part)
		// 裸方向关键字（ASC/DESC 单独成段）不是合法子句：方向必须跟在字段后。
		if upper := strings.ToUpper(part); upper == "ASC" || upper == "DESC" {
			return false
		}
		if !validOrderClausePattern.MatchString(part) {
			return false
		}
	}
	return true
}

// isValidFieldList 报告逗号分隔的字段串是否全部段都在字段白名单内
// （GroupBy 用）。
func isValidFieldList(s string) bool {
	if strings.TrimSpace(s) == "" {
		return false
	}
	for _, part := range strings.Split(s, ",") {
		if !isValidField(strings.TrimSpace(part)) {
			return false
		}
	}
	return true
}

// isValidAssocPath 报告 Preload 关联路径是否合法：点分标识符
// （Profile / Orders.Items）。
func isValidAssocPath(assoc string) bool {
	if assoc == "" {
		return false
	}
	for _, part := range strings.Split(assoc, ".") {
		if !isValidField(part) {
			return false
		}
	}
	return true
}

// buildCondition 将 (field, op, value) 解析为 SQL 片段与参数。
// 返回 ok=false 表示无法解析（字段名不在标识符白名单、操作符不在白名单，
// 或 BETWEEN/NOT BETWEEN 值形状非法），调用方必须以 1=0 fail-closed 处理 ——
// 静默丢弃条件可能让租户/权限过滤凭空消失（数据暴露面），绝不允许 fail-open。
//
// BETWEEN/NOT BETWEEN 支持三种值形状：
//
//	q.Where("age", "BETWEEN", [2]any{18, 65})
//	q.Where("age", "BETWEEN", []any{18, 65})
//	q.Where("age", "BETWEEN", 18, 65) // 两个连续变参
func buildCondition(field, op string, value []any) (query string, args []any, ok bool) {
	if !isValidField(field) {
		return "", nil, false
	}
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
	case "FIND_IN_SET":
		// FIND_IN_SET(?, field) > 0：逗号串列（如 MySQL 标签列）的成员测试，
		// 针值在前、列在后（与下游手写形态 FIND_IN_SET(?, tags) > 0 对齐）。
		// 针值必须是单个标量：GORM 会把 slice 参数展开成 (?, ?, ...)，
		// 多值/切片/nil 一律按值形状非法 fail-closed —— 不重复 IN 多值
		// 被 single() 静默截断的旧错。
		if len(value) != 1 || value[0] == nil {
			return "", nil, false
		}
		if v, isBytes := value[0].([]byte); isBytes {
			// []byte 按单值绑定（驱动层不展开）
			return fmt.Sprintf("FIND_IN_SET(?, %s) > 0", field), []any{v}, true
		}
		switch reflect.TypeOf(value[0]).Kind() {
		case reflect.Slice, reflect.Array:
			return "", nil, false
		}
		return fmt.Sprintf("FIND_IN_SET(?, %s) > 0", field), []any{value[0]}, true
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

// recordFailClosed 记录一次 fail-closed 渲染（#135 信号面）。数据安全面维持
// 既有纪律（条件渲染为恒假 1=0，绝不 fail-open）——调用点必须先把恒假条件
// 追加进链再调本方法，宿主即使 recover 了严格模式的 panic，渲染结果仍安全；
// 本方法只补调用方信号面：无论是否开启严格模式都累计进 Err()（宿主可在
// 启动期把 repo 装配层的条件全量过一遍校验），Strict() 开启时立即 panic
// （把「静默失效」变「当场爆炸」，供开发/CI 环境使用）。
func (q *Query) recordFailClosed(reason string) {
	err := errors.New(reason)
	q.failClosedErrs = append(q.failClosedErrs, err)
	if q.strict {
		panic(err)
	}
}

// Strict 开启严格模式（#135）：链上任何一处条件无法识别而 fail-closed 渲染
// 为恒假 1=0 时立即 panic——把「静默失效」变「当场爆炸」。供开发/CI 环境在
// 启动期或单测里把 repo 装配层的条件全量过一遍校验（典型场景：反引号保留字
// 列名误传入 Where，升级后静默变 WHERE 1=0、接口全线 not-found——严格模式
// 下当场 panic 指明 field/op）；生产环境不要开启（fail-closed 渲染仍是数据
// 安全面的最后防线，严格模式只补信号面）。
//
// 严格模式随 WhereGroup/OrGroup 透传进组内子 Query；无论是否开启，每次
// fail-closed 渲染都经 Err() 可读。链式方法返回 *Query 自身，可与既有链混用：
//
//	q := crud.NewQuery().Strict().Where("account_id", "=", 7)
func (q *Query) Strict() *Query {
	q.strict = true
	return q
}

// Err 返回链上累计的 fail-closed 错误（nil = 所有条件都合法可识别）。
// 无论是否 Strict 都累计——调用方可在不开启 panic 的前提下于启动期/单测
// 把 repo 装配层的条件全量过一遍校验（#135）：渲染 1=0 时行为与「字段合法
// 但值不匹配」不可区分是问题根源，本出口把两者区分开。多条错误 errors.Join
// 合并，信息指明出问题的 field/op 与处置建议（保留字列名走 WhereRaw）。
func (q *Query) Err() error {
	if len(q.failClosedErrs) == 0 {
		return nil
	}
	return errors.Join(q.failClosedErrs...)
}

// Where 添加 AND 条件。支持的操作符（白名单）:
//
//	q.Where("name", "=", "test")
//	q.Where("age", ">", 18)
//	q.Where("status", "IN", []int{1,2,3})
//	q.Where("name", "LIKE", "%test%")
//	q.Where("name", "ILIKE", "%test%")
//	q.Where("tags", "FIND_IN_SET", "go") // FIND_IN_SET(?, tags) > 0（MySQL 逗号串列）
//	q.Where("deleted_at", "IS NULL", nil)
//	q.Where("age", "BETWEEN", 18, 65)       // 两个连续变参
//	q.Where("age", "BETWEEN", []any{18, 65}) // len==2 切片
//
// field 必须是标识符白名单内的**裸标识符**（^[A-Za-z_][A-Za-z0-9_.]*$，含
// 表限定名）：field 会拼入 SQL 片段，前端可控的「筛选/排序字段名」透传进来
// 就是 SQL 注入。**保留字/反引号列名（如 `key`）不在白名单内**——需要引用
// 保留字时请走 WhereRaw（占位符参数化，见其文档），不要给 field 加反引号。
// 白名单外的字段名、未知操作符、非法的 BETWEEN/NOT BETWEEN 值形状一律
// fail-closed：条件渲染为恒假的 1=0（绝不静默丢弃 —— 丢弃租户/权限过滤
// 会造成数据暴露）；渲染发生时经 Err() 累计、Strict() 模式下当场 panic
// （#135 信号面——否则恒假条件与「字段合法但值不匹配」不可区分）。
func (q *Query) Where(field, op string, value ...any) *Query {
	query, args, ok := buildCondition(field, op, value)
	if !ok {
		q.conds = append(q.conds, Condition{Query: failClosedCondition})
		q.recordFailClosed(fmt.Sprintf("crud: Where(%q, %q) 未通过字段/操作符白名单，已 fail-closed 渲染为恒假 %s（保留字/反引号列名请走 WhereRaw 占位符参数化，field 仅接受裸标识符）", field, op, failClosedCondition))
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
// 与 Where 相同的字段名标识符白名单、操作符白名单与值形状约束；无法识别的
// OR 分支同样 fail-closed 渲染为 1=0（该分支不命中任何行），绝不静默丢弃；
// 渲染发生时经 Err() 累计、Strict() 模式下当场 panic（#135，同 Where）。
func (q *Query) Or(field, op string, value ...any) *Query {
	query, args, ok := buildCondition(field, op, value)
	if !ok {
		q.orConds = append(q.orConds, Condition{Query: failClosedCondition})
		q.recordFailClosed(fmt.Sprintf("crud: Or(%q, %q) 未通过字段/操作符白名单，已 fail-closed 渲染为恒假 %s（保留字/反引号列名请走 WhereRaw 占位符参数化，field 仅接受裸标识符）", field, op, failClosedCondition))
		return q
	}
	q.orConds = append(q.orConds, Condition{Query: query, Args: args})
	return q
}

// WhereRaw 添加显式 raw SQL AND 条件（多占位符 + 多参数绑定）。
//
// 与 Where 的 field 内插路径分开：条件里需要拼表达式（自带 ?）时用 WhereRaw
// 显式声明，避免把 raw 表达式塞进 field 造成占位符与参数错位漏绑。片段自动
// 包一层括号（参数绑定不变），片段内部的 OR 不会击穿 AND 链优先级。
//
// **保留字/反引号列名的正道**（#135）：Where 的 field 仅接受裸标识符，
// 把带反引号的保留字列名（`key`）传给 Where 会静默渲染为恒假 1=0；
// 保留字列名应走 WhereRaw（`key` = ? 的片段形态）——片段自带 ?，值仍走
// 占位符参数化。
//
// sql 是调用方审计过的可信 SQL 片段（值仍走占位符参数化，禁止把外部输入
// 拼进 sql 本身）；占位符数量与 args 的对应由调用方保证。空片段无法解析，
// fail-closed 渲染为恒假的 1=0（与未知操作符同一纪律），经 Err() 累计、
// Strict() 模式下当场 panic（#135）。
//
//	q.WhereRaw("expire_date IS NULL OR expire_date > ?", now)
//	// => WHERE (expire_date IS NULL OR expire_date > ?)
func (q *Query) WhereRaw(sql string, args ...any) *Query {
	if strings.TrimSpace(sql) == "" {
		q.conds = append(q.conds, Condition{Query: failClosedCondition})
		q.recordFailClosed(fmt.Sprintf("crud: WhereRaw 收到空片段，已 fail-closed 渲染为恒假 %s", failClosedCondition))
		return q
	}
	q.conds = append(q.conds, Condition{Query: "(" + sql + ")", Args: args})
	return q
}

// WhereGroup 在 AND 链中插入显式括号分组，支持组内 OR（A AND (B OR C)）。
//
// fn 在独立子 Query 上构建组内条件（组内 Where/Or 语义与顶层一致：Where
// 条件间 AND、Or 分支整体 OR），渲染为单个带括号条件并入 AND 链 —— 组内 OR
// 不会击穿链上其它过滤（此前只能全局 Or，SQL 恒为 (A AND B) OR C）。
//
// 组内 fail-closed 与顶层同口径（#135）：Strict() 开启时组内坏条件同样当场
// panic，Err() 同样把组内的 fail-closed 渲染并入父链。
//
// fn 返回 nil 时按传入的子 Query 处理；组内未添加任何条件时为 no-op
// （与条件为 false 时跳过的语义一致，不凭空加恒假条件）。
//
//	q.Where("account_id", "=", 7).
//	    WhereGroup(func(g *Query) *Query {
//	        return g.Where("status", "=", "ok").Or("name", "LIKE", "%x%")
//	    })
//	// => WHERE account_id = ? AND (status = ? OR name LIKE ?)
func (q *Query) WhereGroup(fn func(g *Query) *Query) *Query {
	if c, ok := buildGroup(q, fn); ok {
		q.conds = append(q.conds, c)
	}
	return q
}

// OrGroup 在 OR 链中插入显式括号分组（… OR (B AND C)）。
// 分组整体作为一个 OR 分支，语义与 WhereGroup 相同。
//
//	q.Where("account_id", "=", 7).
//	    OrGroup(func(g *Query) *Query {
//	        return g.Where("status", "=", "ok").Where("name", "LIKE", "%x%")
//	    })
//	// => WHERE account_id = ? OR (status = ? AND name LIKE ?)
func (q *Query) OrGroup(fn func(g *Query) *Query) *Query {
	if c, ok := buildGroup(q, fn); ok {
		q.orConds = append(q.orConds, c)
	}
	return q
}

// buildGroup 在独立子 Query 上执行 fn，把组内条件物化为单个带括号条件。
// 返回 ok=false 表示组内没有任何条件（空组为 no-op，不向链上追加条件）。
// 父链的 strict 标记透传进组内子 Query、组内的 fail-closed 错误并入父链
// （#135：组内坏条件与顶层同口径，信号面不因分组而丢失）。
func buildGroup(parent *Query, fn func(g *Query) *Query) (Condition, bool) {
	g := NewQuery()
	g.strict = parent.strict
	if fn != nil {
		if ret := fn(g); ret != nil {
			g = ret
		}
	}
	parent.failClosedErrs = append(parent.failClosedErrs, g.failClosedErrs...)
	return g.groupCondition()
}

// groupCondition 把组内条件渲染为单个带括号条件（参数按渲染顺序收集，
// Conds 在前、OrConds 在后）。组内语义与顶层一致：Where 条件间 AND，
// Or 分支整体 OR（(A AND B) OR C）。叶子条件不含顶层 OR（buildCondition
// 产物）或自带括号（WhereRaw/嵌套分组），拼接不会被 SQL 优先级击穿。
func (q *Query) groupCondition() (Condition, bool) {
	if len(q.conds) == 0 && len(q.orConds) == 0 {
		return Condition{}, false
	}
	inner := ""
	var args []any
	appendList := func(list []Condition, sep string) {
		for _, c := range list {
			if inner != "" {
				inner += sep
			}
			inner += c.Query
			args = append(args, c.Args...)
		}
	}
	appendList(q.conds, " AND ")
	appendList(q.orConds, " OR ")
	return Condition{Query: "(" + inner + ")", Args: args}, true
}

// Order 设置排序。
//
//	q.Order("created_at DESC")
//	q.Order("name ASC")
//	q.Order("a DESC, b ASC")
//
// order 必须是「字段 [ASC|DESC]」白名单内的排序子句（可逗号分隔多段，
// 字段名为裸标识符、可表限定）：Order 的字符串整串直通后端 raw 拼接面
// （GORM Order 即裸拼），前端可控的「排序字段名」透传进来就是 SQL 注入
// （#154——Where/Or 有 field/op 双白名单，排序面此前零校验）。白名单外的
// 排序整条丢弃（fail-closed：排序不含过滤语义，丢弃即安全）并按 #135 口径
// 经 Err() 累计、Strict() 当场 panic；表达式排序（RAND() 等）不在支持面内。
func (q *Query) Order(order string) *Query {
	if !isValidOrderList(order) {
		q.recordFailClosed(fmt.Sprintf("crud: Order(%q) 未通过排序白名单（字段 [ASC|DESC]，可逗号分隔多段），已丢弃——排序字符串直通后端 raw 拼接，前端可控字段名透传即 SQL 注入（#154）", order))
		return q
	}
	q.orders = append(q.orders, order)
	return q
}

// Preload 预加载关联。
//
// assoc 必须是点分标识符关联路径（Profile / Orders.Items）：白名单外的
// 关联名丢弃并按 #135 口径经 Err() 累计、Strict() 当场 panic（#154，与
// Order 同防线）。args 为调用方审计过的查询条件（同 WhereRaw 口径，值走
// 占位符参数化）。
func (q *Query) Preload(assoc string, args ...any) *Query {
	if !isValidAssocPath(assoc) {
		q.recordFailClosed(fmt.Sprintf("crud: Preload(%q) 未通过关联名白名单（点分标识符路径），已丢弃（#154）", assoc))
		return q
	}
	q.preloads = append(q.preloads, Preload{Assoc: assoc, Args: args})
	return q
}

// Select 选择特定字段。
//
// 每个字段必须在投影白名单内（裸标识符、可表限定，或通配 * / t.*）；
// 白名单外的字段逐个丢弃并按 #135 口径经 Err() 累计、Strict() 当场 panic
// （#154）。聚合表达式（COUNT(*)、子查询等）是 raw SQL 片段，不支持经
// Select 透传。
func (q *Query) Select(fields ...string) *Query {
	for _, f := range fields {
		if !validSelectPattern.MatchString(f) {
			q.recordFailClosed(fmt.Sprintf("crud: Select(%q) 未通过投影白名单（裸标识符 / * / t.*），已丢弃（#154）", f))
			continue
		}
		q.selects = append(q.selects, f)
	}
	return q
}

// Omit 排除特定字段。
//
// 字段名走标识符白名单（同 Where 的 field，#154 收口补齐本方法）：白名单
// 外的值（非法/拼错的排除列名）丢弃并经 recordFailClosed 累计进 Err()、
// Strict() 模式下当场 panic——此前 Omit 是 Query 构建器里唯一不设防的列名
// 入口，非法列名零信号静默失效（想排除的列仍被读出/写穿），与 Select/
// Order/GroupBy/Preload 的 fail-closed 口径相反。Omit 列名只与 schema 字段
// 名匹配、不拼入 SQL，无注入面——本条是信号面与 #154 一致性收口。
func (q *Query) Omit(fields ...string) *Query {
	for _, f := range fields {
		if !isValidField(f) {
			q.recordFailClosed(fmt.Sprintf("crud: Omit(%q) 未通过字段白名单（裸标识符），已丢弃（#154）", f))
			continue
		}
		q.omits = append(q.omits, f)
	}
	return q
}

// Page 设置分页。
//
// 入口归一（#155）：page<1 归 1、pageSize<1 归缺省 20、超上限钳到 500
// （pkg/pagination.Normalize 同一口径）——GetPage/Spec.Page 回写**生效值**，
// 响应信封与仓储层 Offset()/Limit() 的实际 LIMIT/OFFSET 换算一致。此前
// Page(0, 0)（或负值）原样存 NewPageRequest(0,0)：仓储层内部归一打出
// LIMIT 20，service.List 信封却按原始 0/0「未分页」上报——调用方拿 20 条
// 以为拿到全量（#155）。未分页（未调用 Page）语义不变：信封 0/0。
func (q *Query) Page(page, pageSize int) *Query {
	pr := pagination.NewPageRequest(page, pageSize)
	pr.Normalize()
	q.page = pr
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
//
// field 必须是字段白名单内的标识符（可逗号分隔多段）：GroupBy 的字符串
// 同样直通后端 raw 拼接面，白名单外整条丢弃并按 #135 口径经 Err() 累计、
// Strict() 当场 panic（#154，与 Order 同防线）。
func (q *Query) GroupBy(field string) *Query {
	if !isValidFieldList(field) {
		q.recordFailClosed(fmt.Sprintf("crud: GroupBy(%q) 未通过字段白名单（裸标识符，可逗号分隔多段），已丢弃（#154）", field))
		return q
	}
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
