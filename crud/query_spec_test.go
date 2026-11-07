package crud

import "testing"

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
