package gormrepo

import (
	"context"

	"errors"
	"github.com/oekit/gormx/crud"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	bizerrors "github.com/oekit/pkg/errors"
	"github.com/oekit/pkg/pagination"
	"gorm.io/gorm"
)

// setupServiceTestDB creates a fresh sqlite DB with the testUser table.
func setupServiceTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&testUser{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

// userCreateReq / userUpdateReq are the request DTOs used in tests.
type userCreateReq struct {
	Name  string `json:"name"`
	Age   int    `json:"age"`
	Email string `json:"email"`
}

type userUpdateReq struct {
	Name  string `json:"name"`
	Email string `json:"email"`
}

// --- crud.NewBaseService + Set* setters ---

// TestNewBaseService verifies the constructor and that the Set* setters
// are chainable (return the receiver for fluent API).
func TestNewBaseService(t *testing.T) {
	db := setupServiceTestDB(t)
	repo := NewGormRepository[testUser, int64](db)
	svc := crud.NewBaseService[testUser, int64, userCreateReq, userUpdateReq](repo)
	if svc == nil {
		t.Fatal("expected non-nil service")
	}

	// Verify the chainable Set* setters.
	svc.SetToEntity(func(r *userCreateReq) *testUser {
		return &testUser{Name: r.Name, Age: r.Age, Email: r.Email}
	}).SetUpdateEntity(func(e *testUser, r *userUpdateReq) {
		e.Name = r.Name
		e.Email = r.Email
	})
}

// --- Create ---

// TestBaseService_Create_HappyPath covers the happy path: toEntity
// converts the request, repo.Create persists, no hooks.
func TestBaseService_Create_HappyPath(t *testing.T) {
	db := setupServiceTestDB(t)
	repo := NewGormRepository[testUser, int64](db)
	svc := crud.NewBaseService[testUser, int64, userCreateReq, userUpdateReq](repo).
		SetToEntity(func(r *userCreateReq) *testUser {
			return &testUser{Name: r.Name, Age: r.Age, Email: r.Email}
		})

	got, err := svc.Create(context.Background(), &userCreateReq{Name: "alice", Age: 30, Email: "a@e.com"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if got.ID == 0 {
		t.Error("expected ID populated after Create")
	}
	if got.Name != "alice" {
		t.Errorf("expected name=alice, got %q", got.Name)
	}
}

// TestBaseService_Create_NilToEntity covers the path where toEntity
// is not set. Create should return an error to prevent zero-value inserts.
func TestBaseService_Create_NoToEntity(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	type simpleEntity struct {
		ID   int64 `gorm:"primaryKey;autoIncrement"`
		Note string
	}
	if migrateErr := db.AutoMigrate(&simpleEntity{}); migrateErr != nil {
		t.Fatalf("migrate: %v", err)
	}
	repo := NewGormRepository[simpleEntity, int64](db)
	svc := crud.NewBaseService[simpleEntity, int64, userCreateReq, userUpdateReq](repo)
	// No SetToEntity — Create should fail with a clear error.

	_, err = svc.Create(context.Background(), &userCreateReq{Name: "x"})
	if err == nil {
		t.Fatal("expected error when toEntity is not set")
	}
	if !strings.Contains(err.Error(), "toEntity") {
		t.Errorf("expected error about toEntity, got: %s", err.Error())
	}
}

// TestBaseService_Create_BeforeCreateError covers the path where the
// beforeCreate hook returns an error. The repo.Create must NOT be called.
func TestBaseService_Create_BeforeCreateError(t *testing.T) {
	db := setupServiceTestDB(t)
	repo := NewGormRepository[testUser, int64](db)
	beforeErr := errors.New("validation failed")
	svc := crud.NewBaseService[testUser, int64, userCreateReq, userUpdateReq](repo).
		SetToEntity(func(r *userCreateReq) *testUser { return &testUser{Name: r.Name} }).
		SetBeforeCreate(func(_ context.Context, _ *userCreateReq, _ *testUser) error {
			return beforeErr
		})

	_, err := svc.Create(context.Background(), &userCreateReq{Name: "x"})
	if !errors.Is(err, beforeErr) {
		t.Errorf("expected beforeErr, got %v", err)
	}
}

// TestBaseService_Create_AfterCreateError covers the path where the
// afterCreate hook returns an error AFTER successful Create.
func TestBaseService_Create_AfterCreateError(t *testing.T) {
	db := setupServiceTestDB(t)
	repo := NewGormRepository[testUser, int64](db)
	afterErr := errors.New("publish failed")
	svc := crud.NewBaseService[testUser, int64, userCreateReq, userUpdateReq](repo).
		SetToEntity(func(r *userCreateReq) *testUser { return &testUser{Name: r.Name} }).
		SetAfterCreate(func(_ context.Context, _ *testUser) error {
			return afterErr
		})

	_, err := svc.Create(context.Background(), &userCreateReq{Name: "x"})
	if !errors.Is(err, afterErr) {
		t.Errorf("expected afterErr, got %v", err)
	}
}

// TestBaseService_Create_RepoError covers the path where repo.Create
// returns an error.
func TestBaseService_Create_RepoError(t *testing.T) {
	db := setupServiceTestDB(t)
	repo := NewGormRepository[testUser, int64](db)
	// No SetToEntity → new(T) has all zero values. With Name required,
	// GORM will reject. We use a closed DB to force a different
	// error path that's not type-dependent.
	sqlDB, _ := db.DB()
	_ = sqlDB.Close()

	svc := crud.NewBaseService[testUser, int64, userCreateReq, userUpdateReq](repo).
		SetToEntity(func(r *userCreateReq) *testUser { return &testUser{Name: r.Name} })

	_, err := svc.Create(context.Background(), &userCreateReq{Name: "x"})
	if err == nil {
		t.Fatal("expected error from closed DB, got nil")
	}
}

// --- Get ---

// TestBaseService_Get_HappyPath covers the happy path.
func TestBaseService_Get_HappyPath(t *testing.T) {
	db := setupServiceTestDB(t)
	repo := NewGormRepository[testUser, int64](db)
	svc := crud.NewBaseService[testUser, int64, userCreateReq, userUpdateReq](repo).
		SetToEntity(func(r *userCreateReq) *testUser { return &testUser{Name: r.Name, Age: r.Age, Email: r.Email} })

	created, _ := svc.Create(context.Background(), &userCreateReq{Name: "alice"})

	got, err := svc.Get(context.Background(), created.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.ID != created.ID {
		t.Errorf("expected ID=%d, got %d", created.ID, got.ID)
	}
}

// TestBaseService_Get_NotFound covers the "not found" branch — must
// return a *bizerrors.BizError with HTTP 404.
func TestBaseService_Get_NotFound(t *testing.T) {
	db := setupServiceTestDB(t)
	repo := NewGormRepository[testUser, int64](db)
	svc := crud.NewBaseService[testUser, int64, userCreateReq, userUpdateReq](repo)

	_, err := svc.Get(context.Background(), 9999)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var be *bizerrors.BizError
	if !errors.As(err, &be) {
		t.Fatalf("expected *BizError, got %T: %v", err, err)
	}
	if be.BizCode != 404 || be.HTTPCode != 404 {
		t.Errorf("expected biz/http 404, got biz=%d http=%d", be.BizCode, be.HTTPCode)
	}
}

// --- GetOne ---

// TestBaseService_GetOne_HappyPath covers the GetOne happy path.
func TestBaseService_GetOne_HappyPath(t *testing.T) {
	db := setupServiceTestDB(t)
	repo := NewGormRepository[testUser, int64](db)
	svc := crud.NewBaseService[testUser, int64, userCreateReq, userUpdateReq](repo).
		SetToEntity(func(r *userCreateReq) *testUser { return &testUser{Name: r.Name, Age: r.Age, Email: r.Email} })

	_, _ = svc.Create(context.Background(), &userCreateReq{Name: "alice"})

	got, err := svc.GetOne(context.Background(), crud.NewQuery().Where("name", "=", "alice"))
	if err != nil {
		t.Fatalf("GetOne: %v", err)
	}
	if got.Name != "alice" {
		t.Errorf("expected name=alice, got %q", got.Name)
	}
}

// TestBaseService_GetOne_NotFound covers the "not found" branch.
func TestBaseService_GetOne_NotFound(t *testing.T) {
	db := setupServiceTestDB(t)
	repo := NewGormRepository[testUser, int64](db)
	svc := crud.NewBaseService[testUser, int64, userCreateReq, userUpdateReq](repo)

	_, err := svc.GetOne(context.Background(), crud.NewQuery().Where("name", "=", "nobody"))
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var be *bizerrors.BizError
	if !errors.As(err, &be) {
		t.Fatalf("expected *BizError, got %T", err)
	}
	if be.BizCode != 404 || be.HTTPCode != 404 {
		t.Errorf("expected biz/http 404, got biz=%d http=%d", be.BizCode, be.HTTPCode)
	}
}

// --- List ---

// TestBaseService_List_HappyPath covers the List happy path. 未分页（nil Query）
// 时 Page/PageSize 为 0（诚实字段：不假装默认 Page=1/PageSize=20 误导调用方）。
func TestBaseService_List_HappyPath(t *testing.T) {
	db := setupServiceTestDB(t)
	repo := NewGormRepository[testUser, int64](db)
	svc := crud.NewBaseService[testUser, int64, userCreateReq, userUpdateReq](repo).
		SetToEntity(func(r *userCreateReq) *testUser { return &testUser{Name: r.Name, Age: r.Age, Email: r.Email} })

	// Insert 3 users.
	for _, name := range []string{"a", "b", "c"} {
		_, _ = svc.Create(context.Background(), &userCreateReq{Name: name, Age: 20})
	}

	resp, err := svc.List(context.Background(), nil)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if resp.Total != 3 {
		t.Errorf("expected total=3, got %d", resp.Total)
	}
	if len(resp.Items) != 3 {
		t.Errorf("expected 3 items, got %d", len(resp.Items))
	}
	// 未分页：Page/PageSize = 0 表示"未分页"（诚实字段，回归测试：
	// 此前假装 Page=1/PageSize=20，让调用方误以为全量数据是有界结果）。
	if resp.Page != 0 || resp.PageSize != 0 {
		t.Errorf("expected unpaginated page=0, pageSize=0, got page=%d, pageSize=%d", resp.Page, resp.PageSize)
	}
}

// TestBaseService_List_NilPageHonestFields 回归测试：q 非 nil 但未调用 Page()
// 时，同样视为未分页（Page/PageSize=0）。
func TestBaseService_List_NilPageHonestFields(t *testing.T) {
	db := setupServiceTestDB(t)
	repo := NewGormRepository[testUser, int64](db)
	svc := crud.NewBaseService[testUser, int64, userCreateReq, userUpdateReq](repo).
		SetToEntity(func(r *userCreateReq) *testUser { return &testUser{Name: r.Name} })

	for _, name := range []string{"a", "b"} {
		_, _ = svc.Create(context.Background(), &userCreateReq{Name: name})
	}

	resp, err := svc.List(context.Background(), crud.NewQuery().Where("name", "=", "a"))
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if resp.Page != 0 || resp.PageSize != 0 {
		t.Errorf("expected unpaginated page=0, pageSize=0, got page=%d, pageSize=%d", resp.Page, resp.PageSize)
	}
	if resp.Total != 1 || len(resp.Items) != 1 {
		t.Errorf("expected 1 item, got total=%d items=%d", resp.Total, len(resp.Items))
	}
}

// TestBaseService_List_WithQuery covers the path where Query.GetPage
// returns non-default page settings.
func TestBaseService_List_WithQuery(t *testing.T) {
	db := setupServiceTestDB(t)
	repo := NewGormRepository[testUser, int64](db)
	svc := crud.NewBaseService[testUser, int64, userCreateReq, userUpdateReq](repo).
		SetToEntity(func(r *userCreateReq) *testUser { return &testUser{Name: r.Name, Age: r.Age, Email: r.Email} })

	for _, name := range []string{"a", "b", "c", "d", "e"} {
		_, _ = svc.Create(context.Background(), &userCreateReq{Name: name})
	}

	page := pagination.NewPageRequest(1, 2)
	resp, err := svc.List(context.Background(), crud.NewQuery().Page(1, 2))
	_ = page // keep reference for explicit page value
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if resp.Page != 1 || resp.PageSize != 2 {
		t.Errorf("expected page=1, pageSize=2, got %d/%d", resp.Page, resp.PageSize)
	}
	if len(resp.Items) != 2 {
		t.Errorf("expected 2 items, got %d", len(resp.Items))
	}
}

// --- Update ---

// TestBaseService_Update_HappyPath covers the Update happy path.
func TestBaseService_Update_HappyPath(t *testing.T) {
	db := setupServiceTestDB(t)
	repo := NewGormRepository[testUser, int64](db)
	svc := crud.NewBaseService[testUser, int64, userCreateReq, userUpdateReq](repo).
		SetToEntity(func(r *userCreateReq) *testUser { return &testUser{Name: r.Name, Age: r.Age, Email: r.Email} }).
		SetUpdateEntity(func(e *testUser, r *userUpdateReq) {
			e.Name = r.Name
			e.Email = r.Email
		})

	created, _ := svc.Create(context.Background(), &userCreateReq{Name: "alice", Age: 20})

	updated, err := svc.Update(context.Background(), created.ID, &userUpdateReq{Name: "alice-2", Email: "new@e.com"})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if updated.Name != "alice-2" {
		t.Errorf("expected name=alice-2, got %q", updated.Name)
	}
}

// TestBaseService_Update_NotFound covers the path where the ID
// doesn't exist.
func TestBaseService_Update_NotFound(t *testing.T) {
	db := setupServiceTestDB(t)
	repo := NewGormRepository[testUser, int64](db)
	svc := crud.NewBaseService[testUser, int64, userCreateReq, userUpdateReq](repo)

	_, err := svc.Update(context.Background(), 9999, &userUpdateReq{Name: "x"})
	var be *bizerrors.BizError
	if !errors.As(err, &be) {
		t.Fatalf("expected *BizError, got %T: %v", err, err)
	}
	if be.BizCode != 404 || be.HTTPCode != 404 {
		t.Errorf("expected biz/http 404, got biz=%d http=%d", be.BizCode, be.HTTPCode)
	}
}

// TestBaseService_Update_BeforeUpdateError covers the path where
// the beforeUpdate hook errors before the repo.Update.
func TestBaseService_Update_BeforeUpdateError(t *testing.T) {
	db := setupServiceTestDB(t)
	repo := NewGormRepository[testUser, int64](db)
	beforeErr := errors.New("validation failed")
	svc := crud.NewBaseService[testUser, int64, userCreateReq, userUpdateReq](repo).
		SetToEntity(func(r *userCreateReq) *testUser { return &testUser{Name: r.Name} }).
		SetBeforeUpdate(func(_ context.Context, _ int64, _ *userUpdateReq, _ *testUser) error {
			return beforeErr
		})

	created, _ := svc.Create(context.Background(), &userCreateReq{Name: "x"})
	_, err := svc.Update(context.Background(), created.ID, &userUpdateReq{Name: "y"})
	if !errors.Is(err, beforeErr) {
		t.Errorf("expected beforeErr, got %v", err)
	}
}

// TestBaseService_Update_NoUpdateEntity 回归测试：忘记 SetUpdateEntity 时
// Update 必须返回错误（与 Create 的 SetToEntity 约束对称）——
// 此前会静默执行一次无变更的 Save 并"成功"。
func TestBaseService_Update_NoUpdateEntity(t *testing.T) {
	db := setupServiceTestDB(t)
	repo := NewGormRepository[testUser, int64](db)
	svc := crud.NewBaseService[testUser, int64, userCreateReq, userUpdateReq](repo).
		SetToEntity(func(r *userCreateReq) *testUser { return &testUser{Name: r.Name, Age: r.Age, Email: r.Email} })

	created, _ := svc.Create(context.Background(), &userCreateReq{Name: "alice", Age: 20})

	_, err := svc.Update(context.Background(), created.ID, &userUpdateReq{Name: "bob"})
	if err == nil {
		t.Fatal("expected error when updateEntity is not set, got nil")
	}
	if !strings.Contains(err.Error(), "updateEntity") {
		t.Errorf("expected error about updateEntity, got: %s", err.Error())
	}

	// 记录未被静默修改
	got, getErr := svc.Get(context.Background(), created.ID)
	if getErr != nil {
		t.Fatalf("Get after failed Update: %v", getErr)
	}
	if got.Name != "alice" {
		t.Errorf("expected name unchanged (alice), got %q", got.Name)
	}
}

// --- Delete ---

// TestBaseService_Delete_HappyPath covers the Delete happy path.
func TestBaseService_Delete_HappyPath(t *testing.T) {
	db := setupServiceTestDB(t)
	repo := NewGormRepository[testUser, int64](db)
	svc := crud.NewBaseService[testUser, int64, userCreateReq, userUpdateReq](repo).
		SetToEntity(func(r *userCreateReq) *testUser { return &testUser{Name: r.Name} })

	created, _ := svc.Create(context.Background(), &userCreateReq{Name: "alice"})

	if err := svc.Delete(context.Background(), created.ID); err != nil {
		t.Errorf("Delete: %v", err)
	}
	if _, err := svc.Get(context.Background(), created.ID); err == nil {
		t.Error("expected error after delete, got nil")
	}
}

// TestBaseService_Delete_BeforeDeleteError covers the path where
// the beforeDelete hook errors.
func TestBaseService_Delete_BeforeDeleteError(t *testing.T) {
	db := setupServiceTestDB(t)
	repo := NewGormRepository[testUser, int64](db)
	beforeErr := errors.New("permission denied")
	svc := crud.NewBaseService[testUser, int64, userCreateReq, userUpdateReq](repo).
		SetToEntity(func(r *userCreateReq) *testUser { return &testUser{Name: r.Name} }).
		SetBeforeDelete(func(_ context.Context, _ int64) error {
			return beforeErr
		})

	created, _ := svc.Create(context.Background(), &userCreateReq{Name: "x"})
	if err := svc.Delete(context.Background(), created.ID); !errors.Is(err, beforeErr) {
		t.Errorf("expected beforeErr, got %v", err)
	}
	// Verify the record was NOT deleted.
	if _, err := svc.Get(context.Background(), created.ID); err != nil {
		t.Errorf("expected record to still exist, got error: %v", err)
	}
}

// TestBaseService_Delete_AfterDeleteError covers the path where
// the afterDelete hook errors AFTER the repo.Delete.
func TestBaseService_Delete_AfterDeleteError(t *testing.T) {
	db := setupServiceTestDB(t)
	repo := NewGormRepository[testUser, int64](db)
	afterErr := errors.New("audit log failed")
	svc := crud.NewBaseService[testUser, int64, userCreateReq, userUpdateReq](repo).
		SetToEntity(func(r *userCreateReq) *testUser { return &testUser{Name: r.Name} }).
		SetAfterDelete(func(_ context.Context, _ int64) error {
			return afterErr
		})

	created, _ := svc.Create(context.Background(), &userCreateReq{Name: "x"})
	if err := svc.Delete(context.Background(), created.ID); !errors.Is(err, afterErr) {
		t.Errorf("expected afterErr, got %v", err)
	}
}

// --- Repository: PaginateList (package-level helper) ---

// TestPaginateList_HappyPath covers the package-level PaginateList
// helper (was at 0% before this commit).
func TestPaginateList_HappyPath(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	for _, name := range []string{"a", "b", "c"} {
		_ = NewGormRepository[testUser, int64](db).Create(ctx, &testUser{Name: name, Age: 20})
	}

	req := pagination.NewPageRequest(1, 2)
	resp, err := PaginateList[testUser](db.Model(&testUser{}), req, crud.NewQuery().Where("name", "=", "a"))
	if err != nil {
		t.Fatalf("PaginateList: %v", err)
	}
	if resp.Total < 1 {
		t.Errorf("expected total>=1, got %d", resp.Total)
	}
	if resp.Page != 1 || resp.PageSize != 2 {
		t.Errorf("expected page=1, pageSize=2, got page=%d, pageSize=%d", resp.Page, resp.PageSize)
	}
}

// TestPaginateList_NilQuery covers the path where Query is nil —
// the function must still work (delegates to db directly).
func TestPaginateList_NilQuery(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	_ = NewGormRepository[testUser, int64](db).Create(ctx, &testUser{Name: "a"})

	req := pagination.NewPageRequest(1, 10)
	resp, err := PaginateList[testUser](db.Model(&testUser{}), req, nil)
	if err != nil {
		t.Fatalf("PaginateList: %v", err)
	}
	if resp.Total < 1 {
		t.Errorf("expected total>=1, got %d", resp.Total)
	}
}

// TestBaseService_List_PagesPopulated 钉住 2026-09-06 修复:分页结果必须填
// Pages(ceil(total/pageSize)),原实现漏填导致 crudh 列表 API pages 恒 0。
func TestBaseService_List_PagesPopulated(t *testing.T) {
	db := setupTestDB(t)
	repo := NewGormRepository[testUser, int64](db)
	svc := crud.NewBaseService[testUser, int64, map[string]any, map[string]any](repo)
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		if err := repo.Create(ctx, &testUser{Name: "u", Age: i}); err != nil {
			t.Fatal(err)
		}
	}

	resp, err := svc.List(ctx, crud.NewQuery().Where("name", "=", "u").Page(1, 2))
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if resp.Pages != 3 {
		t.Errorf("Pages = %d, want 3 (total=5, pageSize=2)", resp.Pages)
	}
	if resp.Total != 5 || resp.Page != 1 || resp.PageSize != 2 {
		t.Errorf("pagination fields wrong: total=%d page=%d pageSize=%d", resp.Total, resp.Page, resp.PageSize)
	}

	// 未分页:Pages 与 Page/PageSize 一致保持 0
	full, err := svc.List(ctx, nil)
	if err != nil {
		t.Fatalf("List full: %v", err)
	}
	if full.Pages != 0 || full.Page != 0 || full.PageSize != 0 {
		t.Errorf("unpaged Pages/Page/PageSize = %d/%d/%d, want 0/0/0", full.Pages, full.Page, full.PageSize)
	}
}

// TestBaseService_Update_TxPath_AfterUpdateRollsBack 回归：
// repo 实现 TxRepository 时 Update 走"事务 + 行锁读"。afterUpdate 钩子失败
// 必须整体回滚——钩子不得在提交后执行，避免"钩子报错但写已提交"误导调用方。
func TestBaseService_Update_TxPath_AfterUpdateRollsBack(t *testing.T) {
	db := setupServiceTestDB(t)
	repo := NewGormRepository[testUser, int64](db)
	seed := &testUser{Name: "before", Email: "b@t.com", Age: 1}
	if err := repo.Create(context.Background(), seed); err != nil {
		t.Fatal(err)
	}

	afterErr := errors.New("after hook failed")
	svc := crud.NewBaseService[testUser, int64, userCreateReq, userUpdateReq](repo).
		SetUpdateEntity(func(e *testUser, r *userUpdateReq) {
			e.Name = r.Name
		}).
		SetAfterUpdate(func(ctx context.Context, e *testUser) error {
			return afterErr
		})

	if _, err := svc.Update(context.Background(), seed.ID, &userUpdateReq{Name: "changed"}); !errors.Is(err, afterErr) {
		t.Fatalf("expected afterErr, got %v", err)
	}

	got, err := repo.GetByID(context.Background(), seed.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "before" {
		t.Errorf("update must be rolled back when afterUpdate fails, got Name=%q", got.Name)
	}
}

// TestBaseService_Update_TxPath_Commits 事务路径的正常提交。
func TestBaseService_Update_TxPath_Commits(t *testing.T) {
	db := setupServiceTestDB(t)
	repo := NewGormRepository[testUser, int64](db)
	seed := &testUser{Name: "old", Email: "o@t.com", Age: 2}
	if err := repo.Create(context.Background(), seed); err != nil {
		t.Fatal(err)
	}

	svc := crud.NewBaseService[testUser, int64, userCreateReq, userUpdateReq](repo).
		SetUpdateEntity(func(e *testUser, r *userUpdateReq) {
			e.Name = r.Name
		})

	got, err := svc.Update(context.Background(), seed.ID, &userUpdateReq{Name: "new"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "new" {
		t.Errorf("in-tx entity = %q, want new", got.Name)
	}
	reloaded, err := repo.GetByID(context.Background(), seed.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Name != "new" {
		t.Errorf("persisted Name = %q, want new", reloaded.Name)
	}
}

// TestGormRepository_RunInTx_Rollback RunInTx 基础语义：fn 失败回滚。
func TestGormRepository_RunInTx_Rollback(t *testing.T) {
	db := setupServiceTestDB(t)
	repo := NewGormRepository[testUser, int64](db)

	if err := repo.RunInTx(context.Background(), func(inner crud.Repository[testUser, int64]) error {
		if err := inner.Create(context.Background(), &testUser{Name: "tx", Email: "tx@t.com", Age: 1}); err != nil {
			return err
		}
		return errors.New("abort")
	}); err == nil {
		t.Fatal("expected error")
	}

	var count int64
	db.Model(&testUser{}).Where("name = ?", "tx").Count(&count)
	if count != 0 {
		t.Errorf("tx rollback failed: %d rows persisted", count)
	}
}
