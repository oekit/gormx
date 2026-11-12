package gormrepo

import (
	"context"

	"errors"
	"github.com/oekit/gormx/crud"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

type testUser struct {
	ID    int64  `gorm:"primaryKey;autoIncrement"`
	Name  string `gorm:"size:100"`
	Age   int
	Email string `gorm:"size:200"`
}

func setupTestDB(t *testing.T) *gorm.DB {
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

func TestGormRepository_CreateAndGetByID(t *testing.T) {
	db := setupTestDB(t)
	repo := NewGormRepository[testUser, int64](db)
	ctx := context.Background()

	u := &testUser{Name: "alice", Age: 30, Email: "a@e.com"}
	if err := repo.Create(ctx, u); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if u.ID == 0 {
		t.Fatal("expected ID populated after Create")
	}

	got, err := repo.GetByID(ctx, u.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.Name != "alice" {
		t.Errorf("expected name alice, got %s", got.Name)
	}
}

func TestGormRepository_GetByID_NotFound(t *testing.T) {
	db := setupTestDB(t)
	repo := NewGormRepository[testUser, int64](db)

	if _, err := repo.GetByID(context.Background(), 9999); err == nil {
		t.Error("expected error for non-existent ID")
	}
}

func TestGormRepository_GetByCondition(t *testing.T) {
	db := setupTestDB(t)
	repo := NewGormRepository[testUser, int64](db)
	ctx := context.Background()

	_ = repo.Create(ctx, &testUser{Name: "bob", Age: 25, Email: "b@e.com"})
	_ = repo.Create(ctx, &testUser{Name: "bob", Age: 40, Email: "b2@e.com"})

	// 原始 where 条件（透传给 GORM）
	got, err := repo.GetByCondition(ctx, "name = ? AND age > ?", "bob", 30)
	if err != nil {
		t.Fatalf("GetByCondition: %v", err)
	}
	if got.Age != 40 {
		t.Errorf("expected age 40, got %d", got.Age)
	}
}

func TestGormRepository_GetOne(t *testing.T) {
	db := setupTestDB(t)
	repo := NewGormRepository[testUser, int64](db)
	ctx := context.Background()

	_ = repo.Create(ctx, &testUser{Name: "carol", Age: 20, Email: "c@e.com"})

	q := crud.NewQuery().Where("name", "=", "carol")
	got, err := repo.GetOne(ctx, q)
	if err != nil {
		t.Fatalf("GetOne: %v", err)
	}
	if got.Email != "c@e.com" {
		t.Errorf("expected email c@e.com, got %s", got.Email)
	}
}

func TestGormRepository_List(t *testing.T) {
	db := setupTestDB(t)
	repo := NewGormRepository[testUser, int64](db)
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		_ = repo.Create(ctx, &testUser{Name: "user", Age: i, Email: "e@e.com"})
	}

	list, total, err := repo.List(ctx, nil)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if total != 5 {
		t.Errorf("expected total 5, got %d", total)
	}
	if len(list) != 5 {
		t.Errorf("expected 5 items, got %d", len(list))
	}

	// 带条件查询
	q := crud.NewQuery().Where("age", ">=", 3)
	_, filteredTotal, err := repo.List(ctx, q)
	if err != nil {
		t.Fatalf("List with query: %v", err)
	}
	if filteredTotal != 2 {
		t.Errorf("expected filtered total 2, got %d", filteredTotal)
	}
}

func TestGormRepository_Update(t *testing.T) {
	db := setupTestDB(t)
	repo := NewGormRepository[testUser, int64](db)
	ctx := context.Background()

	u := &testUser{Name: "dave", Age: 50, Email: "d@e.com"}
	_ = repo.Create(ctx, u)

	u.Age = 51
	if _, err := repo.Update(ctx, u); err != nil {
		t.Fatalf("Update: %v", err)
	}
	got, _ := repo.GetByID(ctx, u.ID)
	if got.Age != 51 {
		t.Errorf("expected age 51, got %d", got.Age)
	}
}

func TestGormRepository_UpdateFields(t *testing.T) {
	db := setupTestDB(t)
	repo := NewGormRepository[testUser, int64](db)
	ctx := context.Background()

	u := &testUser{Name: "eve", Age: 30, Email: "e@e.com"}
	_ = repo.Create(ctx, u)

	if _, err := repo.UpdateFields(ctx, u.ID, map[string]any{"age": 31}); err != nil {
		t.Fatalf("UpdateFields: %v", err)
	}
	got, _ := repo.GetByID(ctx, u.ID)
	if got.Age != 31 {
		t.Errorf("expected age 31, got %d", got.Age)
	}
}

func TestGormRepository_Delete(t *testing.T) {
	db := setupTestDB(t)
	repo := NewGormRepository[testUser, int64](db)
	ctx := context.Background()

	u := &testUser{Name: "frank", Age: 40, Email: "f@e.com"}
	_ = repo.Create(ctx, u)

	if _, err := repo.Delete(ctx, u.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := repo.GetByID(ctx, u.ID); err == nil {
		t.Error("expected error after delete")
	}
}

func TestGormRepository_BatchCreate(t *testing.T) {
	db := setupTestDB(t)
	repo := NewGormRepository[testUser, int64](db)
	ctx := context.Background()

	users := []*testUser{
		{Name: "b1", Age: 1},
		{Name: "b2", Age: 2},
	}
	if err := repo.BatchCreate(ctx, users, 0); err != nil {
		t.Fatalf("BatchCreate: %v", err)
	}
	_, total, _ := repo.List(ctx, nil)
	if total != 2 {
		t.Errorf("expected total 2, got %d", total)
	}
}

func TestGormRepository_BatchDelete(t *testing.T) {
	db := setupTestDB(t)
	repo := NewGormRepository[testUser, int64](db)
	ctx := context.Background()

	var ids []int64
	for i := 0; i < 3; i++ {
		u := &testUser{Name: "bd", Age: i}
		_ = repo.Create(ctx, u)
		ids = append(ids, u.ID)
	}

	if _, err := repo.BatchDelete(ctx, ids); err != nil {
		t.Fatalf("BatchDelete: %v", err)
	}
	_, total, _ := repo.List(ctx, nil)
	if total != 0 {
		t.Errorf("expected 0 after batch delete, got %d", total)
	}
}

// TestGormRepository_Write_RowsAffected（#60）：写方法回传受影响行数，
// 「记录不存在」（0 行）与实际改动（>0）在仓储层可区分——found 标志语义
// 由调用方按行数构造（如 zeus rpchandler rows>0 → found）。
func TestGormRepository_Write_RowsAffected(t *testing.T) {
	db := setupTestDB(t)
	repo := NewGormRepository[testUser, int64](db)
	ctx := context.Background()

	u := &testUser{Name: "rows", Age: 1, Email: "r@e.com"}
	if err := repo.Create(ctx, u); err != nil {
		t.Fatal(err)
	}

	rows, err := repo.Update(ctx, u)
	if err != nil {
		t.Fatalf("Update existing: %v", err)
	}
	if rows != 1 {
		t.Errorf("Update existing rows=%d, want 1", rows)
	}

	rows, err = repo.Update(ctx, &testUser{ID: 99999, Name: "ghost"})
	if err != nil {
		t.Fatalf("Update missing: %v", err)
	}
	if rows != 0 {
		t.Errorf("Update missing rows=%d, want 0", rows)
	}

	rows, err = repo.UpdateFields(ctx, u.ID, map[string]any{"name": "rows-2"})
	if err != nil {
		t.Fatalf("UpdateFields existing: %v", err)
	}
	if rows != 1 {
		t.Errorf("UpdateFields existing rows=%d, want 1", rows)
	}

	rows, err = repo.UpdateFields(ctx, 99999, map[string]any{"name": "ghost"})
	if err != nil {
		t.Fatalf("UpdateFields missing: %v", err)
	}
	if rows != 0 {
		t.Errorf("UpdateFields missing rows=%d, want 0", rows)
	}

	rows, err = repo.Delete(ctx, 99999)
	if err != nil {
		t.Fatalf("Delete missing: %v", err)
	}
	if rows != 0 {
		t.Errorf("Delete missing rows=%d, want 0", rows)
	}

	rows, err = repo.Delete(ctx, u.ID)
	if err != nil {
		t.Fatalf("Delete existing: %v", err)
	}
	if rows != 1 {
		t.Errorf("Delete existing rows=%d, want 1", rows)
	}
}

// TestGormRepository_Update_Columns（#61）：UpdateOption{Columns} 列白名单
// ——immutable 列保护：白名单外的列即使传入实体携带差异也不落库；
// 缺省维持全字段覆写（含零值列写穿，Select("*") 语义）。
func TestGormRepository_Update_Columns(t *testing.T) {
	db := setupTestDB(t)
	repo := NewGormRepository[testUser, int64](db)
	ctx := context.Background()

	u := &testUser{Name: "imm", Age: 30, Email: "i@e.com"}
	if err := repo.Create(ctx, u); err != nil {
		t.Fatal(err)
	}

	// 白名单只写 name：age/email 是 immutable 列，携带差异也不落库
	patch := &testUser{ID: u.ID, Name: "imm-2", Age: 99, Email: "evil@e.com"}
	rows, err := repo.Update(ctx, patch, crud.UpdateOption{Columns: []string{"name"}})
	if err != nil {
		t.Fatalf("Update with columns: %v", err)
	}
	if rows != 1 {
		t.Errorf("rows=%d, want 1", rows)
	}
	got, err := repo.GetByID(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "imm-2" {
		t.Errorf("白名单内 name 应更新，got %q", got.Name)
	}
	if got.Age != 30 || got.Email != "i@e.com" {
		t.Errorf("白名单外的列不得覆写，got age=%d email=%q", got.Age, got.Email)
	}

	// 负例：缺省全字段覆写（白名单外的列也写、零值列写穿）
	zero := &testUser{ID: u.ID, Name: "imm-3", Age: 0, Email: ""}
	if rows, updErr := repo.Update(ctx, zero); updErr != nil || rows != 1 {
		t.Fatalf("Update full: rows=%d err=%v", rows, updErr)
	}
	got, err = repo.GetByID(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "imm-3" || got.Age != 0 || got.Email != "" {
		t.Errorf("缺省应全字段覆写（含零值列），got %+v", got)
	}
}

func TestGormRepository_Exists(t *testing.T) {
	db := setupTestDB(t)
	repo := NewGormRepository[testUser, int64](db)
	ctx := context.Background()

	_ = repo.Create(ctx, &testUser{Name: "exists", Age: 1})

	q := crud.NewQuery().Where("name", "=", "exists")
	exists, err := repo.Exists(ctx, q)
	if err != nil {
		t.Fatalf("Exists: %v", err)
	}
	if !exists {
		t.Error("expected exists true")
	}

	q2 := crud.NewQuery().Where("name", "=", "nope")
	exists2, _ := repo.Exists(ctx, q2)
	if exists2 {
		t.Error("expected exists false for missing record")
	}
}

func TestGormRepository_Count(t *testing.T) {
	db := setupTestDB(t)
	repo := NewGormRepository[testUser, int64](db)
	ctx := context.Background()

	_ = repo.Create(ctx, &testUser{Name: "a", Age: 5})
	_ = repo.Create(ctx, &testUser{Name: "b", Age: 15})
	_ = repo.Create(ctx, &testUser{Name: "c", Age: 25})

	q := crud.NewQuery().Where("age", ">", 10)
	count, err := repo.Count(ctx, q)
	if err != nil {
		t.Fatalf("Count: %v", err)
	}
	if count != 2 {
		t.Errorf("expected count 2, got %d", count)
	}
}

func TestGormRepository_WithTx_Commit(t *testing.T) {
	db := setupTestDB(t)
	repo := NewGormRepository[testUser, int64](db)
	ctx := context.Background()

	err := db.Transaction(func(tx *gorm.DB) error {
		txRepo := repo.WithTx(tx)
		return txRepo.Create(ctx, &testUser{Name: "tx-commit", Age: 1})
	})
	if err != nil {
		t.Fatalf("transaction: %v", err)
	}

	exists, _ := repo.Exists(ctx, crud.NewQuery().Where("name", "=", "tx-commit"))
	if !exists {
		t.Error("expected record committed and visible")
	}
}

func TestGormRepository_WithTx_Rollback(t *testing.T) {
	db := setupTestDB(t)
	repo := NewGormRepository[testUser, int64](db)
	ctx := context.Background()

	forceErr := errors.New("force rollback")
	err := db.Transaction(func(tx *gorm.DB) error {
		txRepo := repo.WithTx(tx)
		_ = txRepo.Create(ctx, &testUser{Name: "tx-rollback", Age: 1})
		return forceErr
	})
	if !errors.Is(err, forceErr) {
		t.Fatalf("expected rollback error, got %v", err)
	}

	exists, _ := repo.Exists(ctx, crud.NewQuery().Where("name", "=", "tx-rollback"))
	if exists {
		t.Error("expected record rolled back (not exists)")
	}
}

func TestGormRepository_DB(t *testing.T) {
	db := setupTestDB(t)
	repo := NewGormRepository[testUser, int64](db)

	if repo.DB() != db {
		t.Error("DB() should return the underlying *gorm.DB")
	}
}

// TestGormRepository_List_PagedTotal 回归测试：此前 Count 复用了带 OFFSET/LIMIT
// 的语句，page>=2 时聚合行被 OFFSET 跳过，total 恒为 0。
func TestGormRepository_List_PagedTotal(t *testing.T) {
	db := setupTestDB(t)
	repo := NewGormRepository[testUser, int64](db)
	ctx := context.Background()

	for i := 0; i < 25; i++ {
		if err := repo.Create(ctx, &testUser{Name: "u", Age: i, Email: "p@e.com"}); err != nil {
			t.Fatalf("Create: %v", err)
		}
	}

	list, total, err := repo.List(ctx, crud.NewQuery().Page(2, 10))
	if err != nil {
		t.Fatalf("List page 2: %v", err)
	}
	if len(list) != 10 {
		t.Errorf("expected 10 items on page 2, got %d", len(list))
	}
	if total != 25 {
		t.Errorf("expected total 25 on page 2, got %d", total)
	}

	// 第三页只剩 5 条
	list, total, err = repo.List(ctx, crud.NewQuery().Page(3, 10))
	if err != nil {
		t.Fatalf("List page 3: %v", err)
	}
	if len(list) != 5 || total != 25 {
		t.Errorf("page 3: expected 5 items / total 25, got %d / %d", len(list), total)
	}
}

// TestGormRepository_CountExists_IgnorePaging 验证 Count/Exists 不受分页影响。
func TestGormRepository_CountExists_IgnorePaging(t *testing.T) {
	db := setupTestDB(t)
	repo := NewGormRepository[testUser, int64](db)
	ctx := context.Background()

	for i := 0; i < 15; i++ {
		if err := repo.Create(ctx, &testUser{Name: "u", Age: i, Email: "c@e.com"}); err != nil {
			t.Fatalf("Create: %v", err)
		}
	}

	cnt, err := repo.Count(ctx, crud.NewQuery().Page(2, 10))
	if err != nil {
		t.Fatalf("Count: %v", err)
	}
	if cnt != 15 {
		t.Errorf("expected Count 15, got %d", cnt)
	}

	exists, err := repo.Exists(ctx, crud.NewQuery().Page(2, 10))
	if err != nil {
		t.Fatalf("Exists: %v", err)
	}
	if !exists {
		t.Error("expected Exists true regardless of paging")
	}
}

// customPK 主键列名非 "id"，用于验证 UpdateFields / GetByIDForUpdate 的
// 主键解析。
type customPK struct {
	UID  int64  `gorm:"primaryKey;column:uid;autoIncrement"`
	Name string `gorm:"size:50"`
}

// TestGormRepository_UpdateFields_NonIDPrimaryKey 回归测试：此前硬编码
// "id = ?"，主键列名不是 id 的实体更新会静默匹配 0 行。
func TestGormRepository_UpdateFields_NonIDPrimaryKey(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err = db.AutoMigrate(&customPK{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	repo := NewGormRepository[customPK, int64](db)
	ctx := context.Background()

	e := &customPK{Name: "before"}
	if err = repo.Create(ctx, e); err != nil {
		t.Fatalf("Create: %v", err)
	}

	if _, err = repo.UpdateFields(ctx, e.UID, map[string]any{"name": "after"}); err != nil {
		t.Fatalf("UpdateFields: %v", err)
	}

	got, err := repo.GetByID(ctx, e.UID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.Name != "after" {
		t.Errorf("expected name after, got %s", got.Name)
	}
}

// TestGormRepository_GetByIDForUpdate_NonIDPrimaryKey 回归（#194）：
// GetByIDForUpdate 此前硬编码 Where("id", ...)，主键列名非 id 的实体上直接
// no such column: id——且它是 BaseService.Update 事务路径（crud/service.go
// 行锁读）的唯一入口，上层调用方无法绕过（同文件 UpdateFields 的同款缺陷
// 已作为回归 bug 修过一次，本条是该缺陷类的最后一个硬编码点）。
func TestGormRepository_GetByIDForUpdate_NonIDPrimaryKey(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err = db.AutoMigrate(&customPK{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	repo := NewGormRepository[customPK, int64](db)
	ctx := context.Background()

	e := &customPK{Name: "before"}
	if err = repo.Create(ctx, e); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// 行锁读：主键列名 uid（非 id）经 schema 解析 + 当前方言引用
	got, err := repo.GetByIDForUpdate(ctx, e.UID)
	if err != nil {
		t.Fatalf("GetByIDForUpdate: %v", err)
	}
	if got.Name != "before" {
		t.Errorf("Name = %q, want before", got.Name)
	}
	// 未命中路径：仍走 ErrNotFound（不是 no such column 的 SQL 错）
	if _, gerr := repo.GetByIDForUpdate(ctx, 999999); !errors.Is(gerr, crud.ErrNotFound) {
		t.Errorf("miss want ErrNotFound chain, got %v", gerr)
	}

	// BaseService.Update 事务路径（内部硬调 GetByIDForUpdate）同样必须可用
	svc := crud.NewBaseService[customPK, int64, map[string]any, map[string]any](repo).
		SetToEntity(func(r *map[string]any) *customPK {
			return &customPK{Name: (*r)["name"].(string)}
		}).
		SetUpdateEntity(func(e *customPK, r *map[string]any) { e.Name = (*r)["name"].(string) })
	updReq := map[string]any{"name": "after-tx"}
	updated, err := svc.Update(ctx, e.UID, &updReq)
	if err != nil {
		t.Fatalf("BaseService.Update（事务路径）: %v", err)
	}
	if updated.Name != "after-tx" {
		t.Errorf("updated Name = %q, want after-tx", updated.Name)
	}
}

// TestGormRepository_ExistsCount_ForUpdateNoLeak 钉住 2026-09-06 修复:
// 带 ForUpdate()/OrderBy 的 Query 调 Exists/Count 时,聚合语句不得携带
// FOR UPDATE / ORDER BY(原 applyTo(db,false) 会带上:PostgreSQL 拒绝
// 聚合+锁子句、SQLite 语法报错)。SQLite 下 FOR UPDATE 语法错误可作
// 行为断言。
func TestGormRepository_ExistsCount_ForUpdateNoLeak(t *testing.T) {
	db := setupTestDB(t)
	repo := NewGormRepository[testUser, int64](db)
	ctx := context.Background()
	if err := repo.Create(ctx, &testUser{Name: "bob", Age: 20}); err != nil {
		t.Fatal(err)
	}

	q := crud.NewQuery().Where("name", "=", "bob").ForUpdate().Order("id ASC")

	ok, err := repo.Exists(ctx, q)
	if err != nil {
		t.Fatalf("Exists with ForUpdate query: %v(FOR UPDATE 不应泄漏到聚合语句)", err)
	}
	if !ok {
		t.Error("Exists = false, want true")
	}

	total, err := repo.Count(ctx, q)
	if err != nil {
		t.Fatalf("Count with ForUpdate query: %v", err)
	}
	if total != 1 {
		t.Errorf("Count = %d, want 1", total)
	}
}

// uniqueEmailUser 唯一索引测试模型（gormx#181）。
type uniqueEmailUser struct {
	ID    int64  `gorm:"primaryKey;autoIncrement"`
	Email string `gorm:"size:200;uniqueIndex"`
	Name  string `gorm:"size:100"`
}

func setupUniqueDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&uniqueEmailUser{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

// TestGormRepository_DuplicateKeySentinel 回归（gormx#181）：唯一键冲突经
// 实现层出口统一映射为 crud.ErrDuplicateKey 哨兵（errors.Is 可判）——消费方
// 不再退化到驱动文案子串匹配；原始驱动错误仍在链上（errors.As 可达）；
// 非唯一冲突错误原样透传不受影响。Create / BatchCreate / Update /
// UpdateFields 四个写出口全部覆盖。
func TestGormRepository_DuplicateKeySentinel(t *testing.T) {
	db := setupUniqueDB(t)
	repo := NewGormRepository[uniqueEmailUser, int64](db)
	ctx := context.Background()

	first := &uniqueEmailUser{Email: "dup@e.com", Name: "first"}
	if err := repo.Create(ctx, first); err != nil {
		t.Fatalf("first create: %v", err)
	}

	t.Run("Create", func(t *testing.T) {
		_, err := func() (int64, error) {
			err := repo.Create(ctx, &uniqueEmailUser{Email: "dup@e.com", Name: "second"})
			return 0, err
		}()
		if err == nil || !errors.Is(err, crud.ErrDuplicateKey) {
			t.Fatalf("Create duplicate = %v, want errors.Is crud.ErrDuplicateKey", err)
		}
	})

	t.Run("BatchCreate", func(t *testing.T) {
		err := repo.BatchCreate(ctx, []*uniqueEmailUser{{Email: "x@e.com"}, {Email: "dup@e.com"}}, 10)
		if err == nil || !errors.Is(err, crud.ErrDuplicateKey) {
			t.Fatalf("BatchCreate duplicate = %v, want errors.Is crud.ErrDuplicateKey", err)
		}
	})

	other := &uniqueEmailUser{Email: "other@e.com"}
	if err := repo.Create(ctx, other); err != nil {
		t.Fatalf("seed other: %v", err)
	}
	otherEmail := other.Email
	other.Email = "dup@e.com"

	t.Run("Update", func(t *testing.T) {
		if _, err := repo.Update(ctx, other); err == nil || !errors.Is(err, crud.ErrDuplicateKey) {
			t.Fatalf("Update duplicate = %v, want errors.Is crud.ErrDuplicateKey", err)
		}
	})

	t.Run("UpdateFields", func(t *testing.T) {
		if _, err := repo.UpdateFields(ctx, other.ID, map[string]any{"Email": "dup@e.com"}); err == nil || !errors.Is(err, crud.ErrDuplicateKey) {
			t.Fatalf("UpdateFields duplicate = %v, want errors.Is crud.ErrDuplicateKey", err)
		}
		_ = otherEmail
	})

	// 对照：非唯一冲突错误不被误分型（原样透传，不带哨兵链）
	boom := repo.Create(ctx, nil) // nil entity → 驱动层错误，非唯一冲突
	if boom == nil {
		t.Fatal("nil entity must error")
	}
	if errors.Is(boom, crud.ErrDuplicateKey) {
		t.Fatalf("non-duplicate error must not be mapped to sentinel, got %v", boom)
	}
}

// TestWrapUniqueViolation_KeepsDriverErrorInChain 单元（gormx#181）：双 %w
// 包装让哨兵与原始驱动错误同链——errors.Is 命中哨兵的同时，errors.As 仍可
// 取回原始驱动错误的类型化字段（pgconn.PgError 的码位等下游用法依赖此链）。
type syntheticDriverErr struct{ msg string }

func (e syntheticDriverErr) Error() string { return e.msg }

func TestWrapUniqueViolation_KeepsDriverErrorInChain(t *testing.T) {
	orig := syntheticDriverErr{msg: "UNIQUE constraint failed: t.email"}
	err := wrapUniqueViolation(orig)
	if !errors.Is(err, crud.ErrDuplicateKey) {
		t.Fatalf("errors.Is sentinel failed, got %v", err)
	}
	var got syntheticDriverErr
	if !errors.As(err, &got) || got.msg != orig.msg {
		t.Fatalf("errors.As must reach original driver error, got %v", err)
	}

	// 非唯一冲突：原样透传（同一实例，无哨兵链）
	plain := errors.New("connection refused")
	if wrapUniqueViolation(plain) != plain {
		t.Fatal("non-duplicate error must pass through unchanged")
	}
	if wrapUniqueViolation(nil) != nil {
		t.Fatal("nil must pass through")
	}
}
