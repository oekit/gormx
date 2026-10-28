package gormx

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/google/uuid"
	"github.com/oekit/pkg/pagination"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// newTestDB 打开一个 sqlite 内存 DB, 自动 migrate 提供的 models,
// 返回 *gorm.DB 和 cleanup 函数.
func newTestDB(t *testing.T, models ...any) *gorm.DB {
	t.Helper()
	// 每个测试用唯一 dsn, 避免共享 db
	dsn := "file:" + uuid.New().String() + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(models) > 0 {
		if err := db.AutoMigrate(models...); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		sqlDB, err := db.DB()
		if err == nil {
			_ = sqlDB.Close()
		}
	})
	return db
}

// 测试 model
type TestBase struct {
	BaseModel
	Name string
}

type TestSoftDelete struct {
	SoftDeleteModel
	Title string
}

type TestAudit struct {
	AuditModel
	Name string
}

func TestFilterBuilder_Add_Apply(t *testing.T) {
	fb := NewFilterBuilder()
	if !fb.IsEmpty() {
		t.Error("expected empty FilterBuilder initially")
	}
	fb.Add(true, "name = ?", "alice")
	fb.Add(false, "name = ?", "skipped")
	fb.Add(true, "age > ?", 18)

	if fb.IsEmpty() {
		t.Error("expected non-empty after Add")
	}

	db := newTestDB(t)
	db = fb.Apply(db)
	if db.Error != nil {
		t.Errorf("expected nil error, got %v", db.Error)
	}
}

func TestFilterBuilder_Clauses(t *testing.T) {
	fb := NewFilterBuilder()
	fb.Add(true, "name = ?", "alice")
	fb.Add(true, "age > ?", 18)

	sql, args := fb.Clauses()
	if sql != "name = ? AND age > ?" {
		t.Errorf("expected joined SQL, got %q", sql)
	}
	if len(args) != 2 {
		t.Errorf("expected 2 args, got %d", len(args))
	}
}

func TestFilterBuilder_Empty(t *testing.T) {
	fb := NewFilterBuilder()
	sql, args := fb.Clauses()
	if sql != "" || len(args) != 0 {
		t.Errorf("expected empty clauses, got sql=%q args=%v", sql, args)
	}
}

func TestPaginate_EmptyTable(t *testing.T) {
	db := newTestDB(t, &TestBase{})
	resp, err := Paginate[TestBase](db, &pagination.PageRequest{Page: 1, PageSize: 10}, func(db *gorm.DB) *gorm.DB {
		return db.Model(&TestBase{})
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Total != 0 {
		t.Errorf("expected total 0, got %d", resp.Total)
	}
	if len(resp.Items) != 0 {
		t.Errorf("expected empty list, got %d", len(resp.Items))
	}
	if resp.Pages != 0 {
		t.Errorf("expected pages 0, got %d", resp.Pages)
	}
}

func TestPaginate_FullFlow(t *testing.T) {
	db := newTestDB(t, &TestBase{})

	// 插 25 条
	for i := 0; i < 25; i++ {
		if err := db.Create(&TestBase{Name: "item"}).Error; err != nil {
			t.Fatal(err)
		}
	}

	req := &pagination.PageRequest{Page: 2, PageSize: 10}
	resp, err := Paginate[TestBase](db, req, func(db *gorm.DB) *gorm.DB {
		return db.Model(&TestBase{})
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Total != 25 {
		t.Errorf("expected total 25, got %d", resp.Total)
	}
	if len(resp.Items) != 10 {
		t.Errorf("expected 10 items on page 2, got %d", len(resp.Items))
	}
	if resp.Page != 2 || resp.PageSize != 10 {
		t.Errorf("expected page=2 pageSize=10, got %d/%d", resp.Page, resp.PageSize)
	}
	if resp.Pages != 3 {
		t.Errorf("expected pages 3 (25/10), got %d", resp.Pages)
	}
}

func TestPaginateScope_RealDB(t *testing.T) {
	db := newTestDB(t, &TestBase{})
	for i := 0; i < 5; i++ {
		_ = db.Create(&TestBase{Name: "x"}).Error
	}

	req := &pagination.PageRequest{Page: 1, PageSize: 2}
	var count int64
	if err := db.Model(&TestBase{}).Scopes(PaginateScope(req)).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 5 {
		t.Errorf("expected 5 total, got %d", count)
	}

	var items []TestBase
	if err := db.Model(&TestBase{}).Scopes(PaginateScope(req)).Find(&items).Error; err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Errorf("expected 2 items, got %d", len(items))
	}
}

func TestInTransactionContext_Commit(t *testing.T) {
	db := newTestDB(t, &TestBase{})

	err := InTransactionContext(context.Background(), db, func(tx *gorm.DB) error {
		return tx.Create(&TestBase{Name: "tx-1"}).Error
	})
	if err != nil {
		t.Fatal(err)
	}
	var count int64
	db.Model(&TestBase{}).Count(&count)
	if count != 1 {
		t.Errorf("expected 1 row, got %d", count)
	}
}

func TestInTransactionContext_Rollback(t *testing.T) {
	db := newTestDB(t, &TestBase{})

	sentinel := errors.New("rollback")
	err := InTransactionContext(context.Background(), db, func(tx *gorm.DB) error {
		if err := tx.Create(&TestBase{Name: "tx-1"}).Error; err != nil {
			return err
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Errorf("expected sentinel error, got %v", err)
	}
	var count int64
	db.Model(&TestBase{}).Count(&count)
	if count != 0 {
		t.Errorf("expected 0 rows after rollback, got %d", count)
	}
}

func TestOpen_Sqlite(t *testing.T) {
	// Open with sqlite dialector; verify WithGormOptions（零 tracing 注入）/
	dsn := "file:" + uuid.New().String() + "?mode=memory&cache=shared"
	dialector := sqlite.Open(dsn)

	// 零 tracing 注入 — verify no panic, db usable
	db, err := Open(dialector)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&TestBase{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&TestBase{Name: "x"}).Error; err != nil {
		t.Fatal(err)
	}
}

func TestOpen_WithConnectionPool(t *testing.T) {
	dsn := "file:" + uuid.New().String() + "?mode=memory&cache=shared"
	dialector := sqlite.Open(dsn)

	db, err := Open(dialector,
		WithConnectionPool(2, 5, time.Hour),
	)
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	if got := sqlDB.Stats().MaxOpenConnections; got != 5 {
		t.Errorf("expected MaxOpenConnections=5, got %d", got)
	}
}

func TestOpen_InvalidDialector(t *testing.T) {
	// nil dialector 在 gorm v1.31 里可能不立即返回 error (直到第一次 query),
	// 这里只验证不 panic 且返回的 *gorm.DB 可关闭
	db, err := Open(nil)
	if err != nil {
		// 如果这里就返回 error 也接受
		return
	}
	if db == nil {
		t.Error("expected non-nil db")
	}
	sqlDB, derr := db.DB()
	if derr == nil {
		_ = sqlDB.Close()
	}
}

func TestAuditPlugin_Initialize(t *testing.T) {
	db := newTestDB(t)
	if err := db.Use(NewAuditPlugin(func(ctx context.Context) int64 {
		return 42
	})); err != nil {
		t.Fatal(err)
	}
	// 注册 + 验证 Name
	p := NewAuditPlugin(nil)
	if p.Name() != "audit" {
		t.Errorf("expected Name='audit', got %q", p.Name())
	}
}

func TestAuditPlugin_BeforeCreate_AndBeforeUpdate(t *testing.T) {
	db := newTestDB(t, &TestAudit{})
	if err := db.Use(NewAuditPlugin(func(ctx context.Context) int64 {
		return 42
	})); err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&TestAudit{}); err != nil {
		t.Fatal(err)
	}

	// Create
	u := &TestAudit{Name: "alice"}
	if err := db.WithContext(context.Background()).Create(u).Error; err != nil {
		t.Fatal(err)
	}
	if u.CreatedBy != 42 {
		t.Errorf("expected CreatedBy=42, got %d", u.CreatedBy)
	}

	// Update
	u.Name = "bob"
	if err := db.WithContext(context.Background()).Save(u).Error; err != nil {
		t.Fatal(err)
	}
	if u.UpdatedBy != 42 {
		t.Errorf("expected UpdatedBy=42, got %d", u.UpdatedBy)
	}
}

func TestAuditPlugin_NilUserIDFunc(t *testing.T) {
	db := newTestDB(t, &TestAudit{})
	if err := db.Use(NewAuditPlugin(nil)); err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&TestAudit{}); err != nil {
		t.Fatal(err)
	}
	u := &TestAudit{Name: "no-user"}
	if err := db.Create(u).Error; err != nil {
		t.Fatal(err)
	}
	// userID 应当为 0 (因为 UserIDFunc nil)
	if u.CreatedBy != 0 {
		t.Errorf("expected CreatedBy=0 (nil UserIDFunc), got %d", u.CreatedBy)
	}
}

func TestAuditPlugin_ZeroUserID(t *testing.T) {
	db := newTestDB(t, &TestAudit{})
	if err := db.Use(NewAuditPlugin(func(ctx context.Context) int64 {
		return 0 // explicit zero
	})); err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&TestAudit{}); err != nil {
		t.Fatal(err)
	}
	u := &TestAudit{Name: "zero-user"}
	if err := db.Create(u).Error; err != nil {
		t.Fatal(err)
	}
	if u.CreatedBy != 0 {
		t.Errorf("expected CreatedBy=0, got %d", u.CreatedBy)
	}
}

func TestSingletonRepo_Get_NotFound(t *testing.T) {
	db := newTestDB(t, &TestBase{})
	repo := NewSingletonRepo(db, func() *TestBase {
		return &TestBase{Name: "default"}
	})

	got, err := repo.Get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "default" {
		t.Errorf("expected default Name, got %q", got.Name)
	}
	if got.GetID() != 0 {
		t.Errorf("expected ID 0, got %d", got.GetID())
	}
}

func TestSingletonRepo_Save_AndGet(t *testing.T) {
	db := newTestDB(t, &TestBase{})
	repo := NewSingletonRepo(db, func() *TestBase {
		return &TestBase{Name: "default"}
	})

	cfg := &TestBase{Name: "updated"}
	if err := repo.Save(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.GetID() == 0 {
		t.Error("expected non-zero ID after Save")
	}

	got, err := repo.Get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "updated" {
		t.Errorf("expected Name=updated, got %q", got.Name)
	}
}

func TestSingletonRepo_Save_UpdatesExisting(t *testing.T) {
	db := newTestDB(t, &TestBase{})
	repo := NewSingletonRepo(db, func() *TestBase {
		return &TestBase{Name: "default"}
	})

	// 第一次 Save
	cfg := &TestBase{Name: "first"}
	if err := repo.Save(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	firstID := cfg.GetID()

	// 第二次 Save: 应当 update 同一行
	cfg2 := &TestBase{Name: "second"}
	if err := repo.Save(context.Background(), cfg2); err != nil {
		t.Fatal(err)
	}
	if cfg2.GetID() != firstID {
		t.Errorf("expected same ID (%d), got %d", firstID, cfg2.GetID())
	}

	// 验证 DB 里仍只有 1 行
	var count int64
	db.Model(&TestBase{}).Count(&count)
	if count != 1 {
		t.Errorf("expected 1 row, got %d", count)
	}
}

func TestSingletonRepo_GetOrCreate(t *testing.T) {
	db := newTestDB(t, &TestBase{})
	repo := NewSingletonRepo(db, func() *TestBase {
		return &TestBase{Name: "default"}
	})

	// 第一次 GetOrCreate: 不存在, 创建
	cfg, err := repo.GetOrCreate(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Name != "default" {
		t.Errorf("expected Name=default, got %q", cfg.Name)
	}
	if cfg.GetID() == 0 {
		t.Error("expected non-zero ID after GetOrCreate")
	}

	// 第二次 GetOrCreate: 已存在, 返现有
	cfg2, err := repo.GetOrCreate(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if cfg2.GetID() != cfg.GetID() {
		t.Errorf("expected same ID, got %d vs %d", cfg2.GetID(), cfg.GetID())
	}
}

func TestSoftDelete_Delete(t *testing.T) {
	db := newTestDB(t, &TestSoftDelete{})

	u := &TestSoftDelete{Title: "to-delete"}
	if err := db.Create(u).Error; err != nil {
		t.Fatal(err)
	}

	// Delete 应当触发软删除
	if err := db.Delete(u).Error; err != nil {
		t.Fatal(err)
	}

	// 普通查询应当找不到 (因为有 soft delete scope)
	var found TestSoftDelete
	err := db.First(&found, u.ID).Error
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Errorf("expected ErrRecordNotFound, got %v", err)
	}

	// Unscoped 应当能找到
	var unscoped TestSoftDelete
	if err := db.Unscoped().First(&unscoped, u.ID).Error; err != nil {
		t.Fatal(err)
	}
	if unscoped.DeletedAt.Time.IsZero() {
		t.Error("expected DeletedAt to be set")
	}
}

func TestBaseModel_AutoTimeStamps(t *testing.T) {
	db := newTestDB(t, &TestBase{})

	u := &TestBase{Name: "x"}
	if err := db.Create(u).Error; err != nil {
		t.Fatal(err)
	}
	if u.CreatedAt.IsZero() {
		t.Error("expected CreatedAt to be set")
	}
	if u.UpdatedAt.IsZero() {
		t.Error("expected UpdatedAt to be set")
	}
	originalCreated := u.CreatedAt
	originalUpdated := u.UpdatedAt
	time.Sleep(20 * time.Millisecond)

	// Update 应当更新 UpdatedAt
	u.Name = "y"
	if err := db.Save(u).Error; err != nil {
		t.Fatal(err)
	}
	if !u.CreatedAt.Equal(originalCreated) {
		t.Error("expected CreatedAt to be unchanged")
	}
	if !u.UpdatedAt.After(originalUpdated) {
		t.Error("expected UpdatedAt to be advanced")
	}
}
