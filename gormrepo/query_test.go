package gormrepo

import (
	"context"

	"github.com/oekit/gormx/crud"
	"testing"
)

func TestQuery_Where(t *testing.T) {
	q := crud.NewQuery()
	q.Where("name", "=", "test")
	q.Where("age", ">", 18)

	if q.IsEmpty() {
		t.Error("expected query not to be empty")
	}
}

func TestQuery_Chain(t *testing.T) {
	q := crud.NewQuery().
		Where("status", "=", 1).
		Order("created_at DESC").
		Page(1, 20)

	if q.IsEmpty() {
		t.Error("expected query not to be empty")
	}

	page := q.GetPage()
	if page == nil {
		t.Fatal("expected page to be set")
	}
	if page.Page != 1 {
		t.Errorf("expected page 1, got %d", page.Page)
	}
	if page.PageSize != 20 {
		t.Errorf("expected page size 20, got %d", page.PageSize)
	}
}

func TestQuery_Preload(t *testing.T) {
	q := crud.NewQuery().
		Preload("Orders", "status = ?", 1).
		Where("id", "=", 1)

	if q.IsEmpty() {
		t.Error("expected query not to be empty")
	}
}

func TestQuery_ForUpdate(t *testing.T) {
	q := crud.NewQuery().
		Where("id", "=", 1).
		ForUpdate()

	if q.IsEmpty() {
		t.Error("expected query not to be empty")
	}
}

func TestQuery_GroupBy(t *testing.T) {
	q := crud.NewQuery().
		Where("status", ">", 0).
		GroupBy("category")

	if q.IsEmpty() {
		t.Error("expected query not to be empty")
	}
}

func TestQuery_Select(t *testing.T) {
	q := crud.NewQuery().
		Select("id", "name", "status").
		Where("id", "=", 1)

	if q.IsEmpty() {
		t.Error("expected query not to be empty")
	}
}

func TestQuery_Omit(t *testing.T) {
	q := crud.NewQuery().
		Omit("created_at", "updated_at").
		Where("id", "=", 1)

	if q.IsEmpty() {
		t.Error("expected query not to be empty")
	}
}

func TestQuery_IS_NULL(t *testing.T) {
	q := crud.NewQuery().
		Where("deleted_at", "IS NULL")

	if q.IsEmpty() {
		t.Error("expected query not to be empty")
	}
}

func TestQuery_IN(t *testing.T) {
	q := crud.NewQuery().
		Where("status", "IN", []int{1, 2, 3})

	if q.IsEmpty() {
		t.Error("expected query not to be empty")
	}
}

func TestQuery_LIKE(t *testing.T) {
	q := crud.NewQuery().
		Where("name", "LIKE", "%test%")

	if q.IsEmpty() {
		t.Error("expected query not to be empty")
	}
}

// TestPageResponse 测试分页响应结构
func TestPageResponse(t *testing.T) {
	resp := &crud.PageResponse[any]{
		Items:    []any{},
		Total:    100,
		Page:     1,
		PageSize: 20,
	}

	if resp.Total != 100 {
		t.Errorf("expected total 100, got %d", resp.Total)
	}
	if len(resp.Items) != 0 {
		t.Errorf("expected 0 items, got %d", len(resp.Items))
	}
}

// TestQuery_OrWithWhere 验证 Or 与 Where 组合的真实 OR 语义
// （此前 Or 被静默降级为 AND）。SQL 里 AND 优先级高于 OR，
// `WHERE name = 'bob' AND age > 30 OR age = 20` 语义正确。
func TestQuery_OrWithWhere(t *testing.T) {
	db := setupTestDB(t)
	repo := NewGormRepository[testUser, int64](db)
	ctx := context.Background()

	_ = repo.Create(ctx, &testUser{Name: "bob", Age: 30})
	_ = repo.Create(ctx, &testUser{Name: "bob", Age: 40})
	_ = repo.Create(ctx, &testUser{Name: "carol", Age: 20})

	// name=bob AND age>30 OR age=20 → bob(40) + carol(20)，bob(30) 不应命中
	list, total, err := repo.List(ctx, crud.NewQuery().Where("name", "=", "bob").Where("age", ">", 30).Or("age", "=", 20))
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if total != 2 {
		t.Errorf("expected total 2, got %d", total)
	}
	for _, u := range list {
		if u.Name == "bob" && u.Age == 30 {
			t.Errorf("bob(age=30) should not match: %v", u)
		}
	}
}

// TestQuery_OrOnly 验证仅有 Or 条件时生成有效查询。
func TestQuery_OrOnly(t *testing.T) {
	db := setupTestDB(t)
	repo := NewGormRepository[testUser, int64](db)
	ctx := context.Background()

	_ = repo.Create(ctx, &testUser{Name: "bob", Age: 30})
	_ = repo.Create(ctx, &testUser{Name: "carol", Age: 20})

	list, total, err := repo.List(ctx, crud.NewQuery().Or("age", ">", 25))
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if total != 1 || len(list) != 1 || list[0].Name != "bob" {
		t.Errorf("expected only bob, got total=%d list=%v", total, list)
	}
}
