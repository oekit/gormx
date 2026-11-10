package gormrepo

import (
	"context"
	"testing"

	"github.com/oekit/gormx/crud"
	"github.com/oekit/pkg/pagination"
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

// TestPaginateList_SelectsMustNotPolluteCount 回归（#136）：PaginateList 经
// gormx.Paginate 计数，此前 Count 直接作用在带 Selects 的语句上——GORM 生成
// COUNT(<列>) 而非 count(*)，目标列为 NULL 的行不计入 total ⇒ 同一个
// Select("name") 查询，repo.List（countStatement 口径）得 total=3、
// PaginateList 得 Total=2，且同一响应里 Total=2 而 len(Items)=3 自相矛盾、
// Pages 偏一（第 2 页永远取不到数据、「共 N 条」文案失真）。修复后 Paginate
// 的计数语句丢弃 Selects，同仓两条分页路径口径归一。
func TestPaginateList_SelectsMustNotPolluteCount(t *testing.T) {
	_, db := setupCountDB(t)

	resp, err := PaginateList[testUser](db, pagination.NewPageRequest(1, 10), crud.NewQuery().Select("name"))
	if err != nil {
		t.Fatalf("PaginateList: %v", err)
	}
	if resp.Total != 3 {
		t.Fatalf("Total = %d, want 3（COUNT(name) 漏计 NULL 行即 #136 复发）", resp.Total)
	}
	if len(resp.Items) != 3 {
		t.Fatalf("len(Items) = %d, want 3（Total 与实际返回条数不一致）", len(resp.Items))
	}
	if resp.Pages != 1 {
		t.Fatalf("Pages = %d, want 1（Pages 按 Total 计算，偏一即 #136 复发）", resp.Pages)
	}
}

// TestPaginateList_Page2TotalHonest 回归（#161）：page≥2 时 Total 必须等于
// 全表行数。此前 PaginateList 把 ApplyQuery（withPaging: true）的语句交给
// gormx.Paginate，计数语句带上 `LIMIT 10 OFFSET 10`——聚合单行被 OFFSET
// 跳过 ⇒ Total=0 / Pages=0，而 Items 里确实有第 11~20 条数据；调用方按
// Pages==0 判「没有下一页」，剩余数据永远取不到。修复双侧：count 前剔除
// LIMIT/OFFSET（gormx.Paginate），且 PaginateList 不再把 q 的分页带进语句
// （分页只由 req 施加）。
func TestPaginateList_Page2TotalHonest(t *testing.T) {
	db := setupTestDB(t)
	for range 25 {
		if err := db.Create(&testUser{Name: "u", Age: 1}).Error; err != nil {
			t.Fatal(err)
		}
	}
	q := crud.NewQuery().Page(2, 10)

	resp, err := PaginateList[testUser](db, pagination.NewPageRequest(2, 10), q)
	if err != nil {
		t.Fatalf("PaginateList: %v", err)
	}
	if resp.Total != 25 {
		t.Fatalf("Total = %d, want 25（page≥2 计数带 OFFSET ⇒ 恒 0，#161 复发）", resp.Total)
	}
	if len(resp.Items) != 10 {
		t.Fatalf("len(Items) = %d, want 10", len(resp.Items))
	}
	if resp.Pages != 3 {
		t.Fatalf("Pages = %d, want 3（Pages=0 会让剩余页永远取不到）", resp.Pages)
	}

	// 对照口径：Repository.List 的 countStatement 路径，同一 Query 的 total
	// 必须一致（两条公开 API 不得口径分裂）
	repo := NewGormRepository[testUser, int64](db)
	_, total, err := repo.List(context.Background(), q)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if total != resp.Total {
		t.Fatalf("两条分页路径 total 口径分裂：List=%d PaginateList=%d", total, resp.Total)
	}
}
