package gormrepo

import (
	"context"
	"strings"
	"testing"

	"github.com/oekit/gormx/crud"
	"gorm.io/gorm"
)

// --- M5: BETWEEN 值形状宽容解析 + 操作符白名单 fail-closed ---

// TestQuery_Between_TwoVariadicValues 回归测试（M5）：BETWEEN 支持两个连续变参，
// 条件不再被静默丢弃。
func TestQuery_Between_TwoVariadicValues(t *testing.T) {
	db := setupTestDB(t)
	repo := NewGormRepository[testUser, int64](db)
	ctx := context.Background()

	_ = repo.Create(ctx, &testUser{Name: "young", Age: 10})
	_ = repo.Create(ctx, &testUser{Name: "mid", Age: 30})
	_ = repo.Create(ctx, &testUser{Name: "old", Age: 70})

	list, total, err := repo.List(ctx, crud.NewQuery().Where("age", "BETWEEN", 20, 60))
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if total != 1 || len(list) != 1 || list[0].Name != "mid" {
		t.Errorf("expected only mid, got total=%d list=%v", total, list)
	}
}

// TestQuery_Between_Array2Any 回归测试（M5）：[2]any 形状仍然支持。
func TestQuery_Between_Array2Any(t *testing.T) {
	db := setupTestDB(t)
	repo := NewGormRepository[testUser, int64](db)
	ctx := context.Background()

	_ = repo.Create(ctx, &testUser{Name: "young", Age: 10})
	_ = repo.Create(ctx, &testUser{Name: "mid", Age: 30})

	list, total, err := repo.List(ctx, crud.NewQuery().Where("age", "BETWEEN", [2]any{20, 60}))
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if total != 1 || list[0].Name != "mid" {
		t.Errorf("expected only mid, got total=%d list=%v", total, list)
	}
}

// TestQuery_Between_SliceAny 回归测试（M5）：len==2 的 []any 形状支持
// （此前非 [2]any 的值会被静默丢弃整个条件）。
func TestQuery_Between_SliceAny(t *testing.T) {
	db := setupTestDB(t)
	repo := NewGormRepository[testUser, int64](db)
	ctx := context.Background()

	_ = repo.Create(ctx, &testUser{Name: "young", Age: 10})
	_ = repo.Create(ctx, &testUser{Name: "mid", Age: 30})
	_ = repo.Create(ctx, &testUser{Name: "old", Age: 70})

	list, total, err := repo.List(ctx, crud.NewQuery().Where("age", "BETWEEN", []any{20, 60}))
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if total != 1 || list[0].Name != "mid" {
		t.Errorf("expected only mid, got total=%d list=%v", total, list)
	}
}

// TestQuery_Between_InvalidShape_FailClosed 回归测试（M5）：非法 BETWEEN 值
// 必须 fail-closed（渲染为 1=0），绝不静默丢弃条件 —— 丢弃租户/权限过滤
// 会让本应被过滤的行暴露出来。
func TestQuery_Between_InvalidShape_FailClosed(t *testing.T) {
	db := setupTestDB(t)
	repo := NewGormRepository[testUser, int64](db)
	ctx := context.Background()

	_ = repo.Create(ctx, &testUser{Name: "a", Age: 10})
	_ = repo.Create(ctx, &testUser{Name: "b", Age: 30})

	// 非法形状：单值非 [2]any/[]any → 整个查询返回空（fail-closed）
	list, total, err := repo.List(ctx, crud.NewQuery().Where("age", "BETWEEN", 42))
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if total != 0 || len(list) != 0 {
		t.Errorf("expected fail-closed empty result, got total=%d list=%v", total, list)
	}

	// len==3 的 []any 也是非法形状
	list, total, err = repo.List(ctx, crud.NewQuery().Where("age", "BETWEEN", []any{1, 2, 3}))
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if total != 0 || len(list) != 0 {
		t.Errorf("expected fail-closed empty result, got total=%d list=%v", total, list)
	}
}

// TestQuery_UnknownOp_FailClosed 回归测试（M5）：白名单外的操作符 fail-closed，
// 不允许拼入 SQL。
func TestQuery_UnknownOp_FailClosed(t *testing.T) {
	db := setupTestDB(t)
	repo := NewGormRepository[testUser, int64](db)
	ctx := context.Background()

	_ = repo.Create(ctx, &testUser{Name: "a", Age: 10})

	// 未知操作符 → 1=0，返回空且不 panic、不报 SQL 错误
	list, total, err := repo.List(ctx, crud.NewQuery().Where("age", "REGEXP", ".*"))
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if total != 0 || len(list) != 0 {
		t.Errorf("expected fail-closed empty result, got total=%d list=%v", total, list)
	}
}

// TestQuery_OrInvalidBranch_FailClosed 回归测试（M5）：Or 的非法分支渲染为
// 1=0（该分支不命中任何行），且不吞掉其它 OR 分支的正常语义。
func TestQuery_OrInvalidBranch_FailClosed(t *testing.T) {
	db := setupTestDB(t)
	repo := NewGormRepository[testUser, int64](db)
	ctx := context.Background()

	_ = repo.Create(ctx, &testUser{Name: "bob", Age: 30})
	_ = repo.Create(ctx, &testUser{Name: "carol", Age: 20})

	// 合法 OR 分支命中 bob；非法 OR 分支为 1=0 不命中
	list, total, err := repo.List(ctx, crud.NewQuery().Where("age", ">", 25).Or("age", "BETWEEN", "bad"))
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if total != 1 || len(list) != 1 || list[0].Name != "bob" {
		t.Errorf("expected only bob, got total=%d list=%v", total, list)
	}
}

// TestQuery_NotBetweenAndNotIn 验证 NOT BETWEEN / NOT IN 白名单操作符可用。
func TestQuery_NotBetweenAndNotIn(t *testing.T) {
	db := setupTestDB(t)
	repo := NewGormRepository[testUser, int64](db)
	ctx := context.Background()

	_ = repo.Create(ctx, &testUser{Name: "young", Age: 10})
	_ = repo.Create(ctx, &testUser{Name: "old", Age: 70})

	var list []testUser
	_, total, err := repo.List(ctx, crud.NewQuery().Where("age", "NOT BETWEEN", 20, 60))
	if err != nil {
		t.Fatalf("List NOT BETWEEN: %v", err)
	}
	if total != 2 {
		t.Errorf("NOT BETWEEN: expected 2, got %d", total)
	}

	list, total, err = repo.List(ctx, crud.NewQuery().Where("age", "NOT IN", []int{10}))
	if err != nil {
		t.Fatalf("List NOT IN: %v", err)
	}
	if total != 1 || list[0].Name != "old" {
		t.Errorf("NOT IN: expected only old, got total=%d list=%v", total, list)
	}
}

// --- L: List + GroupBy 的 total 语义 ---

// TestGormRepository_List_GroupBy_Total 回归测试：List + GroupBy 时 total 应为
// 分组数（子查询计数），而非第一组的行数（此前直接 COUNT(*) 按组聚合，
// GORM 只读第一行 → total 恒为第一组大小）。
func TestGormRepository_List_GroupBy_Total(t *testing.T) {
	db := setupTestDB(t)
	repo := NewGormRepository[testUser, int64](db)
	ctx := context.Background()

	// 分组用列不存在，借用 Email 模拟 3 个分组：2 + 2 + 1 行
	_ = repo.Create(ctx, &testUser{Name: "g1a", Email: "grp1"})
	_ = repo.Create(ctx, &testUser{Name: "g1b", Email: "grp1"})
	_ = repo.Create(ctx, &testUser{Name: "g2a", Email: "grp2"})
	_ = repo.Create(ctx, &testUser{Name: "g2b", Email: "grp2"})
	_ = repo.Create(ctx, &testUser{Name: "g3a", Email: "grp3"})

	q := crud.NewQuery().GroupBy("email")
	list, total, err := repo.List(ctx, q)
	if err != nil {
		t.Fatalf("List with GroupBy: %v", err)
	}
	if total != 3 {
		t.Errorf("expected total=3 (group count), got %d", total)
	}
	if len(list) != 3 {
		t.Errorf("expected 3 grouped rows, got %d", len(list))
	}

	// 带分页的分组查询：total 仍为分组数
	q2 := crud.NewQuery().GroupBy("email").Page(1, 2)
	list2, total2, err := repo.List(ctx, q2)
	if err != nil {
		t.Fatalf("List with GroupBy+Page: %v", err)
	}
	if total2 != 3 {
		t.Errorf("expected total=3 with paging, got %d", total2)
	}
	if len(list2) != 2 {
		t.Errorf("expected 2 rows on page 1 (pageSize=2), got %d", len(list2))
	}

	// 不带 GroupBy 时 total 仍为行数（原语义不回退）
	_, total3, err := repo.List(ctx, crud.NewQuery().Where("email", "LIKE", "grp%"))
	if err != nil {
		t.Fatalf("List without GroupBy: %v", err)
	}
	if total3 != 5 {
		t.Errorf("expected total=5 without GroupBy, got %d", total3)
	}
}

// TestQuery_In_TwoVariadicValues 回归测试：IN 多值变参此前被 single()
// 静默截断为第一个值（IN (1,2,3) 退化成 IN (1)），其余条件凭空消失。
func TestQuery_In_TwoVariadicValues(t *testing.T) {
	db := setupTestDB(t)
	repo := NewGormRepository[testUser, int64](db)
	ctx := context.Background()

	_ = repo.Create(ctx, &testUser{Name: "a", Age: 10})
	_ = repo.Create(ctx, &testUser{Name: "b", Age: 30})
	_ = repo.Create(ctx, &testUser{Name: "c", Age: 70})

	list, total, err := repo.List(ctx, crud.NewQuery().Where("age", "IN", 30, 70))
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if total != 2 || len(list) != 2 {
		t.Fatalf("expected 2 rows, got total=%d list=%v", total, list)
	}
	for _, u := range list {
		if u.Name == "a" {
			t.Errorf("age=10 should be filtered out, got %+v", u)
		}
	}
}

// TestQuery_NotIn_TwoVariadicValues 同上，验证 NOT IN 的多值变参。
func TestQuery_NotIn_TwoVariadicValues(t *testing.T) {
	db := setupTestDB(t)
	repo := NewGormRepository[testUser, int64](db)
	ctx := context.Background()

	_ = repo.Create(ctx, &testUser{Name: "a", Age: 10})
	_ = repo.Create(ctx, &testUser{Name: "b", Age: 30})

	list, total, err := repo.List(ctx, crud.NewQuery().Where("age", "NOT IN", 10, 99))
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if total != 1 || len(list) != 1 || list[0].Name != "b" {
		t.Errorf("expected only b, got total=%d list=%v", total, list)
	}
}

// --- G: OR 分组 / FIND_IN_SET / WhereRaw（#63 #64）---

// seedGroupFixture 插入越权判别数据集：x/y 同属账户 7，y 在账户 9 的同名行
// 是「OR 右侧绕过 AND 过滤」时会漏出来的越权行。
func seedGroupFixture(t *testing.T, repo *GormRepository[testUser, int64]) {
	t.Helper()
	ctx := context.Background()
	_ = repo.Create(ctx, &testUser{Name: "x", Age: 7}) // 账户 7，命中 B
	_ = repo.Create(ctx, &testUser{Name: "y", Age: 7}) // 账户 7，命中 C
	_ = repo.Create(ctx, &testUser{Name: "y", Age: 9}) // 其它账户仅命中 C —— 不得出现
}

// TestQuery_WhereGroup_A_And_OrGroup 回归测试（#63 #64）：A AND (B OR C)
// 只返回分组命中的行 —— OR 右侧不得绕过 AND 过滤（越权行必须被滤掉）。
func TestQuery_WhereGroup_A_And_OrGroup(t *testing.T) {
	db := setupTestDB(t)
	repo := NewGormRepository[testUser, int64](db)
	seedGroupFixture(t, repo)

	q := crud.NewQuery().
		Where("age", "=", 7).
		WhereGroup(func(g *crud.Query) *crud.Query {
			return g.Or("name", "=", "x").Or("name", "=", "y")
		})

	list, total, err := repo.List(context.Background(), q)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if total != 2 || len(list) != 2 {
		t.Fatalf("expected 2 rows, got total=%d list=%v", total, list)
	}
	for _, u := range list {
		if u.Age != 7 {
			t.Errorf("越权行泄漏（OR 绕过 AND 过滤）：%+v", u)
		}
	}
}

// TestQuery_WhereRaw_MultiArgs 回归测试（#64）：WhereRaw 多占位符多参数绑定，
// 片段自动括号后 OR 不击穿 AND 链（同一越权负例）。
func TestQuery_WhereRaw_MultiArgs(t *testing.T) {
	db := setupTestDB(t)
	repo := NewGormRepository[testUser, int64](db)
	seedGroupFixture(t, repo)

	q := crud.NewQuery().
		Where("age", "=", 7).
		WhereRaw("name = ? OR name = ?", "x", "y")

	list, total, err := repo.List(context.Background(), q)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if total != 2 || len(list) != 2 {
		t.Fatalf("expected 2 rows, got total=%d list=%v", total, list)
	}
	for _, u := range list {
		if u.Age != 7 {
			t.Errorf("越权行泄漏（raw OR 绕过 AND 过滤）：%+v", u)
		}
	}
}

// TestQuery_Or_GlobalUnchanged 对照（#64 反面）：不带分组的 Or() 仍是全局
// OR（(A AND B) OR C）—— 同一数据集上越权行会通过，差异只来自显式分组。
func TestQuery_Or_GlobalUnchanged(t *testing.T) {
	db := setupTestDB(t)
	repo := NewGormRepository[testUser, int64](db)
	seedGroupFixture(t, repo)

	q := crud.NewQuery().
		Where("age", "=", 7).
		Where("name", "=", "x").
		Or("name", "=", "y")

	_, total, err := repo.List(context.Background(), q)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if total != 3 {
		t.Errorf("全局 Or 契约应保持 (A AND B) OR C（3 行），got %d", total)
	}
}

// TestQuery_OrGroup_RealSQL 回归测试（#64）：OrGroup 分组带括号进 OR 链
// （A OR (B AND C)），组内 AND 不把 OR 链切开。
func TestQuery_OrGroup_RealSQL(t *testing.T) {
	db := setupTestDB(t)
	dry := db.Session(&gorm.Session{DryRun: true})
	q := crud.NewQuery().
		Where("age", "=", 7).
		OrGroup(func(g *crud.Query) *crud.Query {
			return g.Where("name", "=", "x").Where("name", "=", "y")
		})

	sql := ApplyQuery(dry, q).Find(&[]testUser{}).Statement.SQL.String()
	if !strings.Contains(sql, "(name = ? AND name = ?)") {
		t.Errorf("OrGroup 应渲染为带括号分组，got %q", sql)
	}
}

// TestQuery_FindInSet_RealSQL 回归测试（#64）：FIND_IN_SET 进白名单后渲染
// FIND_IN_SET(?, field) > 0、针值占位符绑定（sqlite 无 FIND_IN_SET 函数，
// 只做真实 SQL 字符串 + 绑定断言）。
func TestQuery_FindInSet_RealSQL(t *testing.T) {
	db := setupTestDB(t)
	dry := db.Session(&gorm.Session{DryRun: true})
	tx := ApplyQuery(dry, crud.NewQuery().Where("tags", "FIND_IN_SET", "go")).Find(&[]testUser{})

	sql := tx.Statement.SQL.String()
	if !strings.Contains(sql, "FIND_IN_SET(?, tags) > 0") {
		t.Errorf("expected FIND_IN_SET(?, tags) > 0, got %q", sql)
	}
	if len(tx.Statement.Vars) != 1 || tx.Statement.Vars[0] != "go" {
		t.Errorf("needle binding = %v", tx.Statement.Vars)
	}
}

// TestQuery_FindInSet_BadShape_RealSQL_FailClosed（#64 反例）：针值形状非法
// （缺失/多值/切片）与拼错的操作符在真实 SQL 里渲染 1=0，白名单不放行任意片段。
func TestQuery_FindInSet_BadShape_RealSQL_FailClosed(t *testing.T) {
	db := setupTestDB(t)
	cases := []struct {
		name  string
		op    string
		value []any
	}{
		{"missing needle", "FIND_IN_SET", nil},
		{"multi needle", "FIND_IN_SET", []any{"a", "b"}},
		{"slice needle", "FIND_IN_SET", []any{[]string{"a"}}},
		{"typo op", "FIND_IN_SETS", []any{"a"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dry := db.Session(&gorm.Session{DryRun: true})
			sql := ApplyQuery(dry, crud.NewQuery().Where("tags", tc.op, tc.value...)).Find(&[]testUser{}).Statement.SQL.String()
			if !strings.Contains(sql, "1=0") {
				t.Errorf("expected fail-closed 1=0, got %q", sql)
			}
			if strings.Contains(sql, "FIND_IN_SET(") {
				t.Errorf("fail-closed 条件不得拼入 FIND_IN_SET，got %q", sql)
			}
		})
	}
}
