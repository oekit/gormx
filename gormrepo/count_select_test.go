package gormrepo

import (
	"context"
	"testing"

	"github.com/oekit/gormx/crud"
	"gorm.io/gorm"
)

// setupCountDB 建 3 行测试数据：age 分别为 30/31/32，其中 1 行 name 为 NULL。
func setupCountDB(t *testing.T) (*GormRepository[testUser, int64], *gorm.DB) {
	t.Helper()
	db := setupTestDB(t)
	repo := NewGormRepository[testUser, int64](db)
	ctx := context.Background()

	if err := repo.Create(ctx, &testUser{Name: "alice", Age: 30}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := repo.Create(ctx, &testUser{Name: "", Age: 31}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := repo.Create(ctx, &testUser{Name: "bob", Age: 32}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	// 把第 2 行的 name 置成 NULL（COUNT(col) 会跳过 NULL 行）
	if err := db.Exec("UPDATE test_users SET name = NULL WHERE name = ''").Error; err != nil {
		t.Fatalf("update: %v", err)
	}
	return repo, db
}

// TestCount_SelectsMustNotPolluteCount 回归测试：计数语句**不得**被 Selects
// 污染。
//
// 缺陷成因：applySpec 无条件应用 Selects ⇒ GORM 的 Count 在
// Statement.Selects 非空时生成 `COUNT(<列>)` 而不是 `count(*)` ⇒
//   - 该列为 NULL 的行被漏计（实测 3 行含 1 行 name IS NULL ⇒ total 报 2）；
//   - 列名写错时直接 SQL 报错 "no such column"，计数整体失败。
//
// 计数的语义是"行数"，与查哪些列无关。
func TestCount_SelectsMustNotPolluteCount(t *testing.T) {
	repo, _ := setupCountDB(t)
	ctx := context.Background()

	cases := []struct {
		name string
		q    *crud.Query
		want int64
	}{
		{"无 Selects", crud.NewQuery(), 3},
		{"单列 Selects（该列有 NULL 行）", crud.NewQuery().Select("name"), 3},
		{"多列 Selects", crud.NewQuery().Select("name", "age"), 3},
		{"Omits 不影响计数", crud.NewQuery().Omit("name"), 3},
		{"GroupBy 计数", crud.NewQuery().GroupBy("age"), 3},
		{"GroupBy + Selects", crud.NewQuery().GroupBy("age").Select("name"), 3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := repo.Count(ctx, tc.q)
			if err != nil {
				t.Fatalf("Count: %v", err)
			}
			if got != tc.want {
				t.Fatalf("Count = %d，期望 %d（Selects 污染了计数语句）", got, tc.want)
			}
		})
	}
}

// TestCount_SelectsWithUnknownColumn_MustNotFail 回归护栏：Selects 里写了
// 不存在的列时，计数**不应**失败——计数不查列，列名对计数无意义。
//
// 缺陷复现：Selects 被应用后生成 `COUNT(`no_such_col`)` ⇒ SQL 报
// "no such column" ⇒ 整个计数接口不可用。
func TestCount_SelectsWithUnknownColumn_MustNotFail(t *testing.T) {
	repo, _ := setupCountDB(t)

	got, err := repo.Count(context.Background(), crud.NewQuery().Select("no_such_col"))
	if err != nil {
		t.Fatalf("Count 不应因 Selects 里的未知列失败：%v", err)
	}
	if got != 3 {
		t.Fatalf("Count = %d，期望 3", got)
	}
}

// TestExists_SelectsMustNotPolluteCount 回归护栏：Exists 走同一条计数语句，
// 同样不得被 Selects 污染。
func TestExists_SelectsMustNotPolluteCount(t *testing.T) {
	repo, _ := setupCountDB(t)
	ctx := context.Background()

	ok, err := repo.Exists(ctx, crud.NewQuery().Select("name"))
	if err != nil {
		t.Fatalf("Exists: %v", err)
	}
	if !ok {
		t.Fatal("Exists = false，期望 true")
	}
}
