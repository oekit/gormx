package gormrepo

import (
	"context"
	"testing"

	"github.com/oekit/gormx/crud"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// tenantUser 多租户测试模型：行级隔离靠 TenantID。
type tenantUser struct {
	ID       int64  `gorm:"primaryKey;autoIncrement"`
	Name     string `gorm:"size:100"`
	TenantID int64
}

// ctxTenantKey 是测试用的租户身份 ctx key。
type ctxTenantKey struct{}

// withTenant 把租户身份放入 ctx（模拟请求身份）。
func withTenant(ctx context.Context, tid int64) context.Context {
	return context.WithValue(ctx, ctxTenantKey{}, tid)
}

// tenantFrom 取 ctx 中的租户身份；缺失返回 0。
func tenantFrom(ctx context.Context) int64 {
	v, _ := ctx.Value(ctxTenantKey{}).(int64)
	return v
}

// tenantDecorator 标准行级 scope：Where tenant_id = ctx.tenant。
// 缺租户身份时返回 nil（admin/无身份直通——测试 WithScopeRequired 的对照）。
func tenantDecorator(ctx context.Context) func(*gorm.DB) *gorm.DB {
	tid := tenantFrom(ctx)
	if tid == 0 {
		return nil
	}
	return func(db *gorm.DB) *gorm.DB { return db.Where("tenant_id = ?", tid) }
}

func setupTenantDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&tenantUser{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

// seedTenantUsers 预置两个租户各一条记录，返回 (tenant1ID, tenant2ID) 的行主键。
func seedTenantUsers(t *testing.T, db *gorm.DB) (int64, int64) {
	t.Helper()
	u1 := &tenantUser{Name: "alice", TenantID: 1}
	u2 := &tenantUser{Name: "bob", TenantID: 2}
	if err := db.Create(u1).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(u2).Error; err != nil {
		t.Fatal(err)
	}
	return u1.ID, u2.ID
}

// TestQueryDecorator_GetByID_ScopeInjected 按主键 Get 必须过 scope：
// 租户 1 查不到租户 2 的行。
func TestQueryDecorator_GetByID_ScopeInjected(t *testing.T) {
	db := setupTenantDB(t)
	id1, id2 := seedTenantUsers(t, db)
	repo := NewGormRepository[tenantUser, int64](db, WithQueryDecorator[tenantUser](tenantDecorator))

	// 自己租户命中
	got, err := repo.GetByID(withTenant(context.Background(), 1), id1)
	if err != nil {
		t.Fatalf("GetByID own tenant: %v", err)
	}
	if got.Name != "alice" {
		t.Fatalf("expected alice, got %s", got.Name)
	}

	// 他租户被 scope 挡住（不是 ErrRecordNotFound 之外的越权命中）
	_, err = repo.GetByID(withTenant(context.Background(), 1), id2)
	if err == nil {
		t.Fatal("tenant1 不应读到 tenant2 的行")
	}
}

// TestQueryDecorator_UpdateByID_ScopeInjected 按主键 Update 必须过 scope。
func TestQueryDecorator_UpdateByID_ScopeInjected(t *testing.T) {
	db := setupTenantDB(t)
	id1, id2 := seedTenantUsers(t, db)
	repo := NewGormRepository[tenantUser, int64](db, WithQueryDecorator[tenantUser](tenantDecorator))
	ctx := withTenant(context.Background(), 1)

	// 越权 UpdateFields：0 行受影响（GORM 不报错），行内容不变
	if err := repo.UpdateFields(ctx, id2, map[string]any{"name": "hacked"}); err != nil {
		t.Fatalf("UpdateFields cross-tenant: %v", err)
	}
	var row tenantUser
	if err := db.First(&row, id2).Error; err != nil {
		t.Fatal(err)
	}
	if row.Name != "bob" {
		t.Fatalf("越权 UpdateFields 不得改他行，got name=%s", row.Name)
	}

	// 越权 Update：scope 限制下不得命中他行（不用 Save——Save 的 upsert
	// 回退会绕过 scope，见 Update godoc）
	other := &tenantUser{ID: id2, Name: "hacked", TenantID: 2}
	if err := repo.Update(ctx, other); err != nil {
		t.Fatalf("Update cross-tenant: %v", err)
	}
	// 用全新 dest：GORM First 会沿用 dest 已有主键
	row = tenantUser{}
	if err := db.First(&row, id2).Error; err != nil {
		t.Fatal(err)
	}
	if row.Name != "bob" {
		t.Fatalf("越权 Update 不得改他行，got name=%s", row.Name)
	}

	// 自己租户 UpdateFields 成功
	if err := repo.UpdateFields(ctx, id1, map[string]any{"name": "alice2"}); err != nil {
		t.Fatalf("UpdateFields own: %v", err)
	}
	row = tenantUser{}
	if err := db.First(&row, id1).Error; err != nil {
		t.Fatal(err)
	}
	if row.Name != "alice2" {
		t.Fatalf("own UpdateFields 应生效，got %s", row.Name)
	}
}

// TestQueryDecorator_DeleteByID_ScopeInjected 按主键 Delete 必须过 scope。
func TestQueryDecorator_DeleteByID_ScopeInjected(t *testing.T) {
	db := setupTenantDB(t)
	id1, id2 := seedTenantUsers(t, db)
	repo := NewGormRepository[tenantUser, int64](db, WithQueryDecorator[tenantUser](tenantDecorator))
	ctx := withTenant(context.Background(), 1)

	if err := repo.Delete(ctx, id2); err != nil {
		t.Fatalf("Delete cross-tenant: %v", err)
	}
	var count int64
	if err := db.Model(&tenantUser{}).Where("id = ?", id2).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatal("越权 Delete 不得删他行")
	}

	if err := repo.Delete(ctx, id1); err != nil {
		t.Fatalf("Delete own: %v", err)
	}
	if err := db.Model(&tenantUser{}).Where("id = ?", id1).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("own Delete 应删除")
	}
}

// TestQueryDecorator_ListCount_ScopeInjected List/Count 只见本租户。
func TestQueryDecorator_ListCount_ScopeInjected(t *testing.T) {
	db := setupTenantDB(t)
	seedTenantUsers(t, db)
	repo := NewGormRepository[tenantUser, int64](db, WithQueryDecorator[tenantUser](tenantDecorator))
	ctx := withTenant(context.Background(), 1)

	list, total, err := repo.List(ctx, nil)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if total != 1 || len(list) != 1 || list[0].Name != "alice" {
		t.Fatalf("List 应只见本租户，total=%d list=%+v", total, list)
	}

	n, err := repo.Count(ctx, nil)
	if err != nil {
		t.Fatalf("Count: %v", err)
	}
	if n != 1 {
		t.Fatalf("Count 应只见本租户，got %d", n)
	}

	exists, err := repo.Exists(ctx, crud.NewQuery().Where("name", "=", "bob"))
	if err != nil {
		t.Fatalf("Exists: %v", err)
	}
	if exists {
		t.Fatal("Exists 不应看到他租户行")
	}
}

// TestQueryDecorator_NilScope_Passthrough decorator 返回 nil = 本次不加 scope
// （admin 直通）。
func TestQueryDecorator_NilScope_Passthrough(t *testing.T) {
	db := setupTenantDB(t)
	id1, id2 := seedTenantUsers(t, db)
	repo := NewGormRepository[tenantUser, int64](db, WithQueryDecorator[tenantUser](tenantDecorator))

	// ctx 无租户身份 → decorator 返回 nil → passthrough，能读到全部
	ctx := context.Background()
	if _, err := repo.GetByID(ctx, id1); err != nil {
		t.Fatalf("passthrough GetByID id1: %v", err)
	}
	if _, err := repo.GetByID(ctx, id2); err != nil {
		t.Fatalf("passthrough GetByID id2: %v", err)
	}
	list, total, err := repo.List(ctx, nil)
	if err != nil || total != 2 || len(list) != 2 {
		t.Fatalf("passthrough List 应见两行，total=%d list=%+v err=%v", total, list, err)
	}
}

// TestQueryDecorator_DefaultNoDecorator_Passthrough 默认无 decorator 放行
// （单租户零负担）。
func TestQueryDecorator_DefaultNoDecorator_Passthrough(t *testing.T) {
	db := setupTenantDB(t)
	id1, id2 := seedTenantUsers(t, db)
	repo := NewGormRepository[tenantUser, int64](db)
	ctx := context.Background()

	if _, err := repo.GetByID(ctx, id1); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.GetByID(ctx, id2); err != nil {
		t.Fatal(err)
	}
}

// TestScopeRequired_NoDecorator_Errors fail-closed：未配置 decorator 时报错。
func TestScopeRequired_NoDecorator_Errors(t *testing.T) {
	db := setupTenantDB(t)
	id1, _ := seedTenantUsers(t, db)
	repo := NewGormRepository[tenantUser, int64](db, WithScopeRequired[tenantUser]())
	ctx := context.Background()

	if _, err := repo.GetByID(ctx, id1); err == nil {
		t.Fatal("WithScopeRequired 且无 decorator 应报错")
	}
	if _, _, err := repo.List(ctx, nil); err == nil {
		t.Fatal("List 应报错")
	}
	if _, err := repo.Count(ctx, nil); err == nil {
		t.Fatal("Count 应报错")
	}
	if err := repo.Delete(ctx, id1); err == nil {
		t.Fatal("Delete 应报错")
	}
}

// TestScopeRequired_NilScope_Errors fail-closed：decorator 返回 nil（裸查询）报错。
func TestScopeRequired_NilScope_Errors(t *testing.T) {
	db := setupTenantDB(t)
	id1, _ := seedTenantUsers(t, db)
	repo := NewGormRepository[tenantUser, int64](db,
		WithQueryDecorator[tenantUser](tenantDecorator),
		WithScopeRequired[tenantUser](),
	)
	// 无租户身份 → decorator 返回 nil → fail-closed
	ctx := context.Background()

	if _, err := repo.GetByID(ctx, id1); err == nil {
		t.Fatal("WithScopeRequired 拒绝裸查询（nil scope）")
	}
}

// TestScopeRequired_EmptyScope_OK admin 直通的推荐姿势：返回空 scope（非 nil）。
func TestScopeRequired_EmptyScope_OK(t *testing.T) {
	db := setupTenantDB(t)
	id1, id2 := seedTenantUsers(t, db)
	repo := NewGormRepository[tenantUser, int64](db,
		WithQueryDecorator[tenantUser](func(ctx context.Context) func(*gorm.DB) *gorm.DB {
			if tenantFrom(ctx) == 0 {
				// admin：显式空 scope（非 nil），fail-closed 也放行
				return func(db *gorm.DB) *gorm.DB { return db }
			}
			return tenantDecorator(ctx)
		}),
		WithScopeRequired[tenantUser](),
	)
	ctx := context.Background()
	if _, err := repo.GetByID(ctx, id1); err != nil {
		t.Fatalf("空 scope 应放行: %v", err)
	}
	if _, err := repo.GetByID(ctx, id2); err != nil {
		t.Fatalf("空 scope 应放行: %v", err)
	}
	// 有身份时仍隔离
	if _, err := repo.GetByID(withTenant(ctx, 1), id2); err == nil {
		t.Fatal("有身份时仍应隔离")
	}
}

// TestQueryDecorator_RunInTx 继承：事务仓储同样自动注入 scope。
func TestQueryDecorator_RunInTx(t *testing.T) {
	db := setupTenantDB(t)
	id1, id2 := seedTenantUsers(t, db)
	repo := NewGormRepository[tenantUser, int64](db, WithQueryDecorator[tenantUser](tenantDecorator))
	ctx := withTenant(context.Background(), 1)

	err := repo.RunInTx(ctx, func(txRepo crud.Repository[tenantUser, int64]) error {
		// 事务内读：他租户被挡
		if _, err := txRepo.GetByID(ctx, id2); err == nil {
			t.Error("tx 内 GetByID 不应越权")
		}
		// 事务内行锁读（sqlite 退化普通读，但仍过 scope）
		if txg, ok := txRepo.(crud.TxRepository[tenantUser, int64]); ok {
			if _, err := txg.GetByIDForUpdate(ctx, id2); err == nil {
				t.Error("tx 内 GetByIDForUpdate 不应越权")
			}
		}
		// 事务内写他行：被 scope 挡（0 行）
		if err := txRepo.UpdateFields(ctx, id2, map[string]any{"name": "hacked"}); err != nil {
			return err
		}
		return txRepo.UpdateFields(ctx, id1, map[string]any{"name": "in-tx"})
	})
	if err != nil {
		t.Fatalf("RunInTx: %v", err)
	}

	var row tenantUser
	if err := db.First(&row, id2).Error; err != nil {
		t.Fatal(err)
	}
	if row.Name != "bob" {
		t.Fatalf("tx 内越权 UpdateFields 不得生效，got %s", row.Name)
	}
	row = tenantUser{}
	if err := db.First(&row, id1).Error; err != nil {
		t.Fatal(err)
	}
	if row.Name != "in-tx" {
		t.Fatalf("tx 内 own 写应提交，got %s", row.Name)
	}
}

// TestQueryDecorator_WithTx 继承：WithTx 副本同样带 scope。
func TestQueryDecorator_WithTx(t *testing.T) {
	db := setupTenantDB(t)
	id1, id2 := seedTenantUsers(t, db)
	repo := NewGormRepository[tenantUser, int64](db, WithQueryDecorator[tenantUser](tenantDecorator))
	ctx := withTenant(context.Background(), 1)

	if _, err := repo.WithTx(db).GetByID(ctx, id2); err == nil {
		t.Fatal("WithTx 副本应继承 scope")
	}
	if _, err := repo.WithTx(db).GetByID(ctx, id1); err != nil {
		t.Fatalf("own 行应命中: %v", err)
	}
}
