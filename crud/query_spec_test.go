package crud

import (
	"strings"
	"testing"
)

// TestQuerySpec_Assembly 验证中性 Spec 物化：AND/OR/排序/预加载/分页/锁
// 各字段按链式调用顺序收集，值全部占位符参数化。
func TestQuerySpec_Assembly(t *testing.T) {
	q := NewQuery().
		Where("name", "=", "test").
		Where("age", ">", 18).
		Or("status", "=", "disabled").
		Order("created_at DESC").
		Preload("Profile").
		Select("id", "name").
		Omit("password").
		GroupBy("tenant_id").
		Page(2, 20).
		ForUpdate()

	spec := q.Spec()

	if len(spec.Conds) != 2 {
		t.Fatalf("expected 2 AND conds, got %d", len(spec.Conds))
	}
	if spec.Conds[0].Query != "name = ?" || spec.Conds[0].Args[0] != "test" {
		t.Errorf("cond[0] = %q %v", spec.Conds[0].Query, spec.Conds[0].Args)
	}
	if spec.Conds[1].Query != "age > ?" || spec.Conds[1].Args[0] != 18 {
		t.Errorf("cond[1] = %q %v", spec.Conds[1].Query, spec.Conds[1].Args)
	}
	if len(spec.OrConds) != 1 || spec.OrConds[0].Query != "status = ?" {
		t.Errorf("orConds = %+v", spec.OrConds)
	}
	if len(spec.Orders) != 1 || spec.Orders[0] != "created_at DESC" {
		t.Errorf("orders = %v", spec.Orders)
	}
	if len(spec.Preloads) != 1 || spec.Preloads[0].Assoc != "Profile" {
		t.Errorf("preloads = %+v", spec.Preloads)
	}
	if len(spec.Selects) != 2 || len(spec.Omits) != 1 {
		t.Errorf("selects=%v omits=%v", spec.Selects, spec.Omits)
	}
	if spec.GroupBy != "tenant_id" {
		t.Errorf("groupBy = %q", spec.GroupBy)
	}
	if spec.Page == nil || spec.Page.Page != 2 || spec.Page.PageSize != 20 {
		t.Errorf("page = %+v", spec.Page)
	}
	if !spec.ForUpdate {
		t.Error("ForUpdate not set")
	}
	if q.IsEmpty() {
		t.Error("IsEmpty() = true for non-empty query")
	}
}

// TestQuerySpec_FailClosed 未知操作符必须渲染为恒假 1=0，绝不静默丢弃。
func TestQuerySpec_FailClosed(t *testing.T) {
	q := NewQuery().Where("age", "; DROP TABLE users; --", 1)
	spec := q.Spec()
	if len(spec.Conds) != 1 || spec.Conds[0].Query != failClosedCondition {
		t.Fatalf("expected fail-closed 1=0, got %+v", spec.Conds)
	}
	if len(spec.Conds[0].Args) != 0 {
		t.Errorf("fail-closed cond should carry no args, got %v", spec.Conds[0].Args)
	}
}

// TestQuerySpec_SnapshotIsolation Spec 返回快照，物化后继续链式修改
// Query 不影响已取得的 Spec。
func TestQuerySpec_SnapshotIsolation(t *testing.T) {
	q := NewQuery().Where("a", "=", 1)
	spec := q.Spec()
	q.Where("b", "=", 2)
	if len(spec.Conds) != 1 {
		t.Errorf("snapshot mutated: %d conds", len(spec.Conds))
	}
	if len(q.Spec().Conds) != 2 {
		t.Errorf("query should have 2 conds after chained Where")
	}
}

// TestQuerySpec_WhereGroup_Parenthesized 回归测试（#64）：OR 分组渲染为单个
// 带括号条件（A AND (B OR C)），组内 OR 不击穿链上其它 AND 过滤。
func TestQuerySpec_WhereGroup_Parenthesized(t *testing.T) {
	q := NewQuery().
		Where("account_id", "=", 7).
		WhereGroup(func(g *Query) *Query {
			return g.Where("status", "=", "ok").Or("name", "LIKE", "%x%")
		})

	spec := q.Spec()
	if len(spec.Conds) != 2 {
		t.Fatalf("expected 2 conds, got %d", len(spec.Conds))
	}
	got := spec.Conds[1]
	if got.Query != "(status = ? OR name LIKE ?)" {
		t.Errorf("group cond = %q, want (status = ? OR name LIKE ?)", got.Query)
	}
	if len(got.Args) != 2 || got.Args[0] != "ok" || got.Args[1] != "%x%" {
		t.Errorf("group args = %v", got.Args)
	}
}

// TestQuerySpec_OrGroup_Parenthesized（#64）：OR 分支上的括号分组
// （A OR (B AND C)），分组整体作为一个 OR 分支。
func TestQuerySpec_OrGroup_Parenthesized(t *testing.T) {
	q := NewQuery().
		Where("account_id", "=", 7).
		OrGroup(func(g *Query) *Query {
			return g.Where("status", "=", "ok").Where("name", "LIKE", "%x%")
		})

	spec := q.Spec()
	if len(spec.OrConds) != 1 {
		t.Fatalf("expected 1 or-cond, got %d", len(spec.OrConds))
	}
	if got := spec.OrConds[0]; got.Query != "(status = ? AND name LIKE ?)" {
		t.Errorf("or-group cond = %q, want (status = ? AND name LIKE ?)", got.Query)
	}
}

// TestQuerySpec_Or_GlobalUnchanged 对照（#64 反面）：不分组的 Or() 仍是全局
// OR 语义（(A AND B) OR C）——既有契约不变，分组是显式 opt-in。
func TestQuerySpec_Or_GlobalUnchanged(t *testing.T) {
	spec := NewQuery().Where("a", "=", 1).Where("b", "=", 2).Or("c", "=", 3).Spec()
	if len(spec.Conds) != 2 || len(spec.OrConds) != 1 {
		t.Fatalf("global Or 形态应保持不变，conds=%d orConds=%d", len(spec.Conds), len(spec.OrConds))
	}
	if spec.OrConds[0].Query != "c = ?" {
		t.Errorf("or cond = %q", spec.OrConds[0].Query)
	}
}

// TestQuerySpec_Group_FailClosedAndNesting（#64 反面）：组内未知操作符仍
// fail-closed 渲染 1=0（分组不放行坏条件）；嵌套分组继续带括号。
func TestQuerySpec_Group_FailClosedAndNesting(t *testing.T) {
	q := NewQuery().WhereGroup(func(g *Query) *Query {
		return g.Where("age", "REGEXP", ".*").OrGroup(func(inner *Query) *Query {
			return inner.Where("name", "=", "x")
		})
	})
	got := q.Spec().Conds[0]
	if got.Query != "(1=0 OR (name = ?))" {
		t.Errorf("group cond = %q, want (1=0 OR (name = ?))", got.Query)
	}
}

// TestQuerySpec_EmptyGroup_Noop 空组是 no-op（与条件为 false 时跳过的语义
// 一致），不凭空追加恒假条件。
func TestQuerySpec_EmptyGroup_Noop(t *testing.T) {
	q := NewQuery().Where("a", "=", 1).WhereGroup(func(g *Query) *Query { return g })
	if spec := q.Spec(); len(spec.Conds) != 1 {
		t.Errorf("empty group should add no cond, got %+v", spec.Conds)
	}
}

// TestQuerySpec_FindInSet（#64 正例）：白名单收录 FIND_IN_SET，形态
// FIND_IN_SET(?, field) > 0（针值在前、列在后），针值占位符绑定。
func TestQuerySpec_FindInSet(t *testing.T) {
	got := NewQuery().Where("tags", "FIND_IN_SET", "go").Spec().Conds[0]
	if got.Query != "FIND_IN_SET(?, tags) > 0" {
		t.Errorf("cond = %q, want FIND_IN_SET(?, tags) > 0", got.Query)
	}
	if len(got.Args) != 1 || got.Args[0] != "go" {
		t.Errorf("needle binding = %v", got.Args)
	}
}

// TestQuerySpec_FindInSet_BadShape_FailClosed（#64 反例）：针值形状非法
// （缺失/多值/切片/nil）与拼错的操作符一律 fail-closed 渲染 1=0 ——
// 白名单新增操作符绝不放行任意片段，也不重复 IN 多值被静默截断的旧错。
func TestQuerySpec_FindInSet_BadShape_FailClosed(t *testing.T) {
	cases := []struct {
		name  string
		op    string
		value []any
	}{
		{"missing needle", "FIND_IN_SET", nil},
		{"multi needle", "FIND_IN_SET", []any{"a", "b"}},
		{"slice needle", "FIND_IN_SET", []any{[]string{"a"}}},
		{"nil needle", "FIND_IN_SET", []any{nil}},
		{"typo op", "FIND_IN_SETS", []any{"a"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := NewQuery().Where("tags", tc.op, tc.value...).Spec().Conds[0]
			if got.Query != failClosedCondition {
				t.Errorf("cond = %q, want %s", got.Query, failClosedCondition)
			}
			if len(got.Args) != 0 {
				t.Errorf("fail-closed cond should carry no args, got %v", got.Args)
			}
		})
	}
}

// TestQuerySpec_WhereRaw（#64 正例）：显式 raw 入口多占位符多参数绑定，
// 片段自动带括号（与 FilterBuilder.Add 同一防击穿机制）。
func TestQuerySpec_WhereRaw(t *testing.T) {
	q := NewQuery().
		Where("account_id", "=", 7).
		WhereRaw("expire_date IS NULL OR expire_date > ?", "2030-01-01")

	got := q.Spec().Conds[1]
	if got.Query != "(expire_date IS NULL OR expire_date > ?)" {
		t.Errorf("raw cond = %q, want auto-parenthesized", got.Query)
	}
	if len(got.Args) != 1 || got.Args[0] != "2030-01-01" {
		t.Errorf("raw args = %v", got.Args)
	}
}

// TestQuerySpec_WhereRaw_Empty_FailClosed（#64 反例）：空 raw 片段无法解析，
// fail-closed 渲染 1=0，绝不静默丢弃。
func TestQuerySpec_WhereRaw_Empty_FailClosed(t *testing.T) {
	got := NewQuery().WhereRaw("  ").Spec().Conds[0]
	if got.Query != failClosedCondition {
		t.Errorf("cond = %q, want %s", got.Query, failClosedCondition)
	}
}

// TestQuerySpec_FieldIdentifierFailClosed 回归（#100）：field 与 op 一样直接
// 拼入 SQL 片段，白名单外的字段名（注入载荷、函数调用、带空格/引号的片段）
// 必须渲染为恒假的 1=0——把前端可控的「筛选/排序字段名」透传给 Where 就是
// 经典 SQL 注入，fail-closed 不能只覆盖操作符面。
func TestQuerySpec_FieldIdentifierFailClosed(t *testing.T) {
	cases := []struct {
		name  string
		field string
	}{
		{"注入载荷", "id; DROP TABLE users; --"},
		{"UNION 注入", "1 UNION SELECT password FROM users"},
		{"函数调用", "COUNT(*)"},
		{"引号片段", "' OR '1'='1"},
		{"带空格", "id DESC"},
		{"数字开头", "1id"},
		{"空字段", ""},
		{"裸点开头", ".id"},
		{"连字符", "user-name"},
		{"注释符", "id/*x*/"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := NewQuery().Where(tc.field, "=", 1).Spec().Conds[0]
			if got.Query != failClosedCondition {
				t.Errorf("Where(%q) cond = %q, want %s", tc.field, got.Query, failClosedCondition)
			}
			if len(got.Args) != 0 {
				t.Errorf("fail-closed cond should carry no args, got %v", got.Args)
			}
			// Or 分支同一防线
			gotOr := NewQuery().Or(tc.field, "=", 1).Spec().OrConds[0]
			if gotOr.Query != failClosedCondition {
				t.Errorf("Or(%q) cond = %q, want %s", tc.field, gotOr.Query, failClosedCondition)
			}
		})
	}
}

// TestQuerySpec_FieldIdentifierAllowed（#100 正例）：常规列名、snake_case、
// 表限定名都在白名单内，既有用法不受影响。
func TestQuerySpec_FieldIdentifierAllowed(t *testing.T) {
	cases := []struct{ field, wantPrefix string }{
		{"id", "id = ?"},
		{"user_name", "user_name = ?"},
		{"t.col", "t.col = ?"},
		{"created_at_2", "created_at_2 = ?"},
	}
	for _, tc := range cases {
		got := NewQuery().Where(tc.field, "=", "v").Spec().Conds[0]
		if got.Query != tc.wantPrefix {
			t.Errorf("Where(%q) cond = %q, want %q", tc.field, got.Query, tc.wantPrefix)
		}
	}
	// FIND_IN_SET 的 field 同样过白名单（列在后位拼接）
	got := NewQuery().Where("tags", "FIND_IN_SET", "go").Spec().Conds[0]
	if got.Query != "FIND_IN_SET(?, tags) > 0" {
		t.Errorf("FIND_IN_SET cond = %q", got.Query)
	}
	injected := NewQuery().Where("tags) > 0 OR (1", "FIND_IN_SET", "go").Spec().Conds[0]
	if injected.Query != failClosedCondition {
		t.Errorf("FIND_IN_SET 字段注入应 fail-closed, got %q", injected.Query)
	}
}

// ---- #135：fail-closed 渲染的调用方信号面（Err 累计 + Strict 当场爆炸） ----

// TestQuery_Err_AccumulatesFailClosed 回归（#135）：反引号保留字列名（zeus
// iam GetByKey 的 `key` 形态）等白名单外字段此前静默渲染成恒假 1=0、
// 无任何信号——行为与「字段合法但值不匹配」不可区分，下游只能靠既有单测或
// 逐仓人工 grep 排查。修复后 Err() 把每次 fail-closed 渲染累计为带 field/op
// 的错误，宿主可在启动期把 repo 装配层的条件全量过一遍校验；数据安全面不变
// （条件仍渲染恒假 1=0）。
func TestQuery_Err_AccumulatesFailClosed(t *testing.T) {
	q := NewQuery().
		Where("`key`", "=", "v1"). // 反引号保留字列名（下游实证形态）
		Where("name", "=", "ok").  // 合法条件不受影响
		Or("b@d", ">", 2)          // 注入载荷

	err := q.Err()
	if err == nil {
		t.Fatal("Err() 应返回累计的 fail-closed 错误（#135）")
	}
	msg := err.Error()
	for _, want := range []string{"`key`", "b@d", failClosedCondition, "WhereRaw"} {
		if !strings.Contains(msg, want) {
			t.Errorf("Err() 信息应提及 %q, got %v", want, msg)
		}
	}

	// 数据安全面不变：坏条件仍渲染恒假 1=0，合法条件照常
	spec := q.Spec()
	if len(spec.Conds) != 2 {
		t.Fatalf("conds = %d, want 2（坏条件 + 合法条件）", len(spec.Conds))
	}
	if spec.Conds[0].Query != failClosedCondition {
		t.Errorf("坏条件应渲染 %s, got %q", failClosedCondition, spec.Conds[0].Query)
	}
	if spec.Conds[1].Query != "name = ?" {
		t.Errorf("合法条件不应受影响, got %q", spec.Conds[1].Query)
	}
	if len(spec.OrConds) != 1 || spec.OrConds[0].Query != failClosedCondition {
		t.Errorf("Or 分支坏条件应渲染 1=0, got %+v", spec.OrConds)
	}
}

// TestQuery_Err_NilWhenAllValid 对照组：全部条件合法时 Err() 为 nil，
// 既有调用方零感知。
func TestQuery_Err_NilWhenAllValid(t *testing.T) {
	q := NewQuery().
		Where("name", "=", "x").
		Where("t.col", "IN", []int{1, 2}).
		Or("age", ">", 18).
		WhereRaw("expire_date IS NULL OR expire_date > ?", "2030-01-01").
		WhereGroup(func(g *Query) *Query {
			return g.Where("status", "=", "ok")
		})
	if err := q.Err(); err != nil {
		t.Errorf("Err() = %v, want nil（全部条件合法）", err)
	}
}

// TestQuery_Strict_PanicsOnUnrecognizableField 严格模式（#135 建议 1）：
// 开发/CI 环境开启即把「静默失效」变「当场爆炸」——panic 携带的错误指明
// 出问题的 field/op。
func TestQuery_Strict_PanicsOnUnrecognizableField(t *testing.T) {
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("Strict 模式下坏条件应 panic（#135）")
		}
		err, ok := r.(error)
		if !ok || !strings.Contains(err.Error(), "`key`") {
			t.Fatalf("panic 应携带提及 field 的错误, got %v", r)
		}
	}()
	NewQuery().Strict().Where("`key`", "=", "v1")
}

// TestQuery_Strict_GroupPropagates 组内坏条件同口径：Strict 随 WhereGroup
// 透传进组内子 Query——组内坏条件同样当场 panic，信号面不因分组而丢失。
func TestQuery_Strict_GroupPropagates(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("Strict 模式下组内坏条件应 panic（#135）")
		}
	}()
	NewQuery().Strict().Where("account_id", "=", 7).WhereGroup(func(g *Query) *Query {
		return g.Where("`key`", "=", "v1")
	})
}

// TestQuery_Err_PropagatesFromGroup 非 Strict 的组内信号面：组内 fail-closed
// 渲染并入父链 Err()，宿主的启动期校验一次覆盖全链（含分组）；组内坏条件
// 渲染出的恒假条件仍带括号并入 AND 链（fail-closed 渲染不变）。
func TestQuery_Err_PropagatesFromGroup(t *testing.T) {
	q := NewQuery().WhereGroup(func(g *Query) *Query {
		return g.Where("1bad", "=", 1)
	})
	err := q.Err()
	if err == nil {
		t.Fatal("组内 fail-closed 渲染应并入父链 Err()（#135）")
	}
	if !strings.Contains(err.Error(), "1bad") {
		t.Errorf("Err() 应提及组内出问题的 field, got %v", err)
	}
	if got := q.Spec().Conds[0].Query; got != "("+failClosedCondition+")" {
		t.Errorf("组内坏条件应渲染为 (%s), got %q", failClosedCondition, got)
	}
}

// TestQuery_Err_WhereRawEmpty WhereRaw 空片段同样有信号面（与未知操作符
// 同一纪律）。
func TestQuery_Err_WhereRawEmpty(t *testing.T) {
	q := NewQuery().WhereRaw("   ")
	if err := q.Err(); err == nil {
		t.Fatal("WhereRaw 空片段应计入 Err()（#135）")
	}
	if got := q.Spec().Conds[0].Query; got != failClosedCondition {
		t.Errorf("cond = %q, want %s", got, failClosedCondition)
	}
}

// ---- #154：Order/GroupBy/Select/Preload 标识符白名单 ----

// TestQuerySpec_Order_InjectionFailClosed 回归（#154）：Where/Or 有 field/op
// 双白名单（#100/#135），但 Order 此前原样内插——gormrepo 把 spec.Orders 整串
// 直接交给 GORM 的 raw Order 拼接，前端可控的「排序字段名」透传进来就是 SQL
// 注入面。白名单（字段 [ASC|DESC]）外的排序必须丢弃（fail-closed，排序不含
// 过滤语义，丢弃即安全）并按 #135 口径经 Err() 累计、Strict() 当场 panic。
func TestQuerySpec_Order_InjectionFailClosed(t *testing.T) {
	cases := []struct{ name, order string }{
		{"注入载荷", "id; DROP TABLE users; --"},
		{"UNION 注入", "id) UNION SELECT password FROM users --"},
		{"函数调用", "RAND()"},
		{"子查询", "(SELECT 1)"},
		{"注释符", "id/*x*/"},
		{"引号片段", "' OR '1'='1"},
		{"空排序", ""},
		{"仅方向", "DESC"},
		{"方向后缀伪关键字", "id DESC; DROP TABLE users; --"},
		{"多段含坏段", "id DESC, name; DROP TABLE users; --"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			q := NewQuery().Order(tc.order)
			spec := q.Spec()
			if len(spec.Orders) != 0 {
				t.Errorf("Order(%q) 应丢弃（原样透传即注入面）, spec.Orders = %v", tc.order, spec.Orders)
			}
			if q.Err() == nil {
				t.Errorf("Order(%q) 应计入 Err() 信号面（#135）", tc.order)
			}
		})
	}
}

// TestQuerySpec_GroupSelectPreload_InjectionFailClosed（#154）：GroupBy/
// Select/Preload(assoc) 与 Order 同一防线——白名单外的值丢弃 + Err() 累计，
// 绝不原样透传。
func TestQuerySpec_GroupSelectPreload_InjectionFailClosed(t *testing.T) {
	t.Run("GroupBy", func(t *testing.T) {
		for _, field := range []string{"id; DROP TABLE users; --", "COUNT(*)", "id, (SELECT 1)", ""} {
			q := NewQuery().GroupBy(field)
			if got := q.Spec().GroupBy; got != "" {
				t.Errorf("GroupBy(%q) 应丢弃, spec.GroupBy = %q", field, got)
			}
			if q.Err() == nil {
				t.Errorf("GroupBy(%q) 应计入 Err()", field)
			}
		}
	})
	t.Run("Select", func(t *testing.T) {
		for _, field := range []string{"id, (SELECT password FROM users)", "id; DROP TABLE users; --", "COUNT(*)"} {
			q := NewQuery().Select(field)
			if got := q.Spec().Selects; len(got) != 0 {
				t.Errorf("Select(%q) 应丢弃, spec.Selects = %v", field, got)
			}
			if q.Err() == nil {
				t.Errorf("Select(%q) 应计入 Err()", field)
			}
		}
	})
	t.Run("Preload", func(t *testing.T) {
		for _, assoc := range []string{"Profile; DROP TABLE users; --", "a'b", "Orders.", ""} {
			q := NewQuery().Preload(assoc)
			if got := q.Spec().Preloads; len(got) != 0 {
				t.Errorf("Preload(%q) 应丢弃, spec.Preloads = %+v", assoc, got)
			}
			if q.Err() == nil {
				t.Errorf("Preload(%q) 应计入 Err()", assoc)
			}
		}
	})
}

// TestQuerySpec_OrderGroupSelectPreload_ValidPassthrough（#154 正例）：
// 合法调用原样透传、不计入 Err()——白名单收紧不得破坏既有用法（向后兼容）。
func TestQuerySpec_OrderGroupSelectPreload_ValidPassthrough(t *testing.T) {
	q := NewQuery().
		Order("created_at DESC").
		Order("name").
		Order("a DESC, b ASC").
		GroupBy("tenant_id").
		Select("id", "name", "t.col", "*", "t.*").
		Preload("Profile").
		Preload("Orders.Items", "status = ?", 1)
	if err := q.Err(); err != nil {
		t.Fatalf("合法调用不应计入 Err(): %v", err)
	}
	spec := q.Spec()
	if len(spec.Orders) != 3 || spec.Orders[0] != "created_at DESC" || spec.Orders[1] != "name" || spec.Orders[2] != "a DESC, b ASC" {
		t.Errorf("orders = %v", spec.Orders)
	}
	if spec.GroupBy != "tenant_id" {
		t.Errorf("groupBy = %q", spec.GroupBy)
	}
	if len(spec.Selects) != 5 {
		t.Errorf("selects = %v", spec.Selects)
	}
	if len(spec.Preloads) != 2 || spec.Preloads[0].Assoc != "Profile" || spec.Preloads[1].Assoc != "Orders.Items" {
		t.Errorf("preloads = %+v", spec.Preloads)
	}
}

// TestQuery_Strict_PanicsOnInjectedOrder 严格模式同口径（#154）：非法排序在
// Strict() 下当场 panic（信号面与 Where/Or 一致，#135）。
func TestQuery_Strict_PanicsOnInjectedOrder(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("Strict 模式下非法 Order 应 panic（#154）")
		}
	}()
	NewQuery().Strict().Order("id; DROP TABLE users; --")
}

// TestQuery_Page_NormalizesAtEntry（#155）：分页入口归一——GetPage/Spec.Page
// 回写生效值，与仓储层 Offset()/Limit() 的归一口径一致（信封不再按原始
// 0/0「未分页」谎报）。
func TestQuery_Page_NormalizesAtEntry(t *testing.T) {
	cases := []struct {
		name               string
		page, size         int
		wantPage, wantSize int
	}{
		{"0/0 按缺省归一", 0, 0, 1, 20},
		{"负值按缺省归一", -3, -1, 1, 20},
		{"页长超上限钳到 500", 1, 1000, 1, 500},
		{"合法值原样", 2, 30, 2, 30},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			q := NewQuery().Page(tc.page, tc.size)
			p := q.GetPage()
			if p == nil {
				t.Fatal("GetPage() = nil")
			}
			if p.Page != tc.wantPage || p.PageSize != tc.wantSize {
				t.Errorf("GetPage() = (%d, %d), want (%d, %d)", p.Page, p.PageSize, tc.wantPage, tc.wantSize)
			}
			spec := q.Spec()
			if spec.Page == nil || spec.Page.Page != tc.wantPage || spec.Page.PageSize != tc.wantSize {
				t.Errorf("spec.Page = %+v, want (%d, %d)", spec.Page, tc.wantPage, tc.wantSize)
			}
			// 与仓储层 LIMIT/OFFSET 换算一致（信封回写值 = 实际生效值）
			if p.Limit() != tc.wantSize {
				t.Errorf("Limit() = %d, want %d", p.Limit(), tc.wantSize)
			}
		})
	}
}

// TestQuerySpec_Omit_IdentifierFailClosed（#196）：Omit 是 #154 标识符白名单
// 收口漏掉的唯一列名入口——非法/拼错的排除列名此前零信号静默失效（Err()
// nil、Strict 不 panic、拼错列仍被读出/写穿），与 Select/Order/GroupBy/
// Preload 的 fail-closed 口径相反。白名单外的值必须丢弃 + Err() 累计，
// 合法字段原样透传（向后兼容）。
func TestQuerySpec_Omit_IdentifierFailClosed(t *testing.T) {
	for _, field := range []string{"id; DROP TABLE users; --", "password, secret", "a-b", "`key`", ""} {
		q := NewQuery().Omit(field)
		if got := q.Spec().Omits; len(got) != 0 {
			t.Errorf("Omit(%q) 应丢弃, spec.Omits = %v", field, got)
		}
		if q.Err() == nil {
			t.Errorf("Omit(%q) 应计入 Err() 信号面（#135 口径）", field)
		}
	}
	// 合法字段（含变参多字段）原样透传
	q := NewQuery().Omit("password", "secret_col", "t.internal")
	if err := q.Err(); err != nil {
		t.Fatalf("合法 Omit 不应计入 Err(): %v", err)
	}
	if got := q.Spec().Omits; len(got) != 3 {
		t.Errorf("合法 Omit 应保留, spec.Omits = %v", got)
	}
	// Strict 当场 panic（与 Where/Select 等同口径）
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("Strict 模式下非法 Omit 应 panic（#135 口径）")
		}
	}()
	NewQuery().Strict().Omit("bad-field!")
}
