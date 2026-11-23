package tenant

// register_test.go —— 强制注入全行为面（glebarez sqlite 内存库，无外部依赖）。

import (
	"context"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

type tenantRow struct {
	ID        int64 `gorm:"primaryKey"`
	TenantID  string
	Note      string
	DeletedAt *time.Time
}

func (tenantRow) TableName() string { return "tenant_rows" }

// globalRow 无租户列/无软删列——全局基础设施表形态，注入必须跳过。
type globalRow struct {
	ID   int64 `gorm:"primaryKey"`
	Name string
}

func (globalRow) TableName() string { return "global_rows" }

func openDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&tenantRow{}, &globalRow{}); err != nil {
		t.Fatal(err)
	}
	return db
}

// TestRegisterForcesTenantPredicate 三租户隔离实查：acme 只见己行、无声明落
// Default 桶、WithoutScope 见全部。
func TestRegisterForcesTenantPredicate(t *testing.T) {
	db := openDB(t)
	if err := Register(db); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	seed := []tenantRow{
		{ID: 1, TenantID: "acme", Note: "a"},
		{ID: 2, TenantID: "acme", Note: "b"},
		{ID: 3, TenantID: "globex", Note: "c"},
	}
	for i := range seed {
		if err := db.WithContext(NewContext(ctx, seed[i].TenantID)).Create(&seed[i]).Error; err != nil {
			t.Fatal(err)
		}
	}

	var acme []tenantRow
	if err := db.WithContext(NewContext(ctx, "acme")).Find(&acme).Error; err != nil {
		t.Fatal(err)
	}
	if len(acme) != 2 {
		t.Fatalf("acme 应见 2 行, got %d", len(acme))
	}

	// 无声明 → Default 桶（零行）：fail-closed 为「无声明=默认租户」而非全可见。
	var def []tenantRow
	if err := db.WithContext(ctx).Find(&def).Error; err != nil {
		t.Fatal(err)
	}
	if len(def) != 0 {
		t.Fatalf("default 桶应 0 行（数据都在 acme/globex）, got %d", len(def))
	}

	// 逃生门：WithoutScope 见全部。
	var all []tenantRow
	if err := db.WithContext(WithoutScope(ctx)).Find(&all).Error; err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 {
		t.Fatalf("WithoutScope 应见 3 行, got %d", len(all))
	}
}

// TestRegisterCreateBackfill 回填语义：tenant_id 零值从 ctx 回填；显式
// 写入优先（跨租户搬运等高级场景不被回调覆盖）。
func TestRegisterCreateBackfill(t *testing.T) {
	db := openDB(t)
	if err := Register(db); err != nil {
		t.Fatal(err)
	}
	ctx := NewContext(context.Background(), "acme")

	var backfilled tenantRow // 零值 TenantID → 回填 acme
	if err := db.WithContext(ctx).Create(&backfilled).Error; err != nil {
		t.Fatal(err)
	}
	if backfilled.TenantID != "acme" {
		t.Fatalf("回填 tenant=%q, want acme", backfilled.TenantID)
	}

	explicit := tenantRow{TenantID: "globex"} // 显式写入优先
	if err := db.WithContext(ctx).Create(&explicit).Error; err != nil {
		t.Fatal(err)
	}
	if explicit.TenantID != "globex" {
		t.Fatalf("显式 tenant=%q 不应被覆盖, got %q", "globex", explicit.TenantID)
	}
}

// TestRegisterSoftDeletePredicate deleted_at 列在场 ⇒ 默认不可见软删行；
// WithDeleted 逃生门看得见。
func TestRegisterSoftDeletePredicate(t *testing.T) {
	db := openDB(t)
	if err := Register(db); err != nil {
		t.Fatal(err)
	}
	ctx := NewContext(context.Background(), "acme")
	now := time.Now()
	rows := []tenantRow{
		{ID: 1, TenantID: "acme", Note: "alive"},
		{ID: 2, TenantID: "acme", Note: "dead", DeletedAt: &now},
	}
	for i := range rows {
		if err := db.WithContext(WithoutScope(ctx)).Create(&rows[i]).Error; err != nil {
			t.Fatal(err)
		}
	}

	var visible []tenantRow
	if err := db.WithContext(ctx).Find(&visible).Error; err != nil {
		t.Fatal(err)
	}
	if len(visible) != 1 || visible[0].Note != "alive" {
		t.Fatalf("默认应只见 alive 行, got %+v", visible)
	}

	var withDeleted []tenantRow
	if err := db.WithContext(WithDeleted(ctx)).Find(&withDeleted).Error; err != nil {
		t.Fatal(err)
	}
	if len(withDeleted) != 2 {
		t.Fatalf("WithDeleted 应见 2 行, got %d", len(withDeleted))
	}
}

// TestRegisterSkipsGlobalTables 无租户列的全局表：注入与回填都不影响。
func TestRegisterSkipsGlobalTables(t *testing.T) {
	db := openDB(t)
	if err := Register(db); err != nil {
		t.Fatal(err)
	}
	ctx := NewContext(context.Background(), "acme")
	if err := db.WithContext(ctx).Create(&globalRow{Name: "g"}).Error; err != nil {
		t.Fatal(err)
	}
	var got []globalRow
	if err := db.WithContext(ctx).Find(&got).Error; err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("全局表应正常读写, got %d", len(got))
	}
}

// TestRegisterFailLoud 重复注册（回调重名）必须报错——半注册 = 隔离静默失效。
func TestRegisterFailLoud(t *testing.T) {
	db := openDB(t)
	if err := Register(db); err != nil {
		t.Fatal(err)
	}
	if err := Register(db); err == nil {
		t.Fatal("重复注册应返回 error（fail-loud）")
	}
	if err := Register(nil); err == nil {
		t.Fatal("nil db 应返回 error")
	}
}

// TestRegisterWithSoftDeleteOff 关掉软删除谓词：宿主自带软删方案时只注入租户谓词。
func TestRegisterWithSoftDeleteOff(t *testing.T) {
	db := openDB(t)
	if err := Register(db, WithSoftDeletePredicate(false)); err != nil {
		t.Fatal(err)
	}
	ctx := NewContext(context.Background(), "acme")
	now := time.Now()
	rows := []tenantRow{
		{ID: 1, TenantID: "acme", Note: "alive"},
		{ID: 2, TenantID: "acme", Note: "dead", DeletedAt: &now},
	}
	for i := range rows {
		if err := db.WithContext(WithoutScope(ctx)).Create(&rows[i]).Error; err != nil {
			t.Fatal(err)
		}
	}
	var visible []tenantRow
	if err := db.WithContext(ctx).Find(&visible).Error; err != nil {
		t.Fatal(err)
	}
	if len(visible) != 2 {
		t.Fatalf("软删谓词关闭后应见 2 行（宿主自负软删过滤）, got %d", len(visible))
	}
}

// TestRegisterUpdateDeleteScoped Update/Delete 同受谓词约束：跨租户改不到、删不到。
func TestRegisterUpdateDeleteScoped(t *testing.T) {
	db := openDB(t)
	if err := Register(db); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, tid := range []string{"acme", "globex"} {
		if err := db.WithContext(NewContext(ctx, tid)).Create(&tenantRow{TenantID: tid, Note: "n"}).Error; err != nil {
			t.Fatal(err)
		}
	}
	// acme 上下文里 Delete「全部」应只删 acme 行。
	res := db.WithContext(NewContext(ctx, "acme")).Where("1 = 1").Delete(&tenantRow{})
	if res.Error != nil {
		t.Fatal(res.Error)
	}
	if res.RowsAffected != 1 {
		t.Fatalf("acme 删除应影响 1 行, got %d", res.RowsAffected)
	}
	var remaining []tenantRow
	if err := db.WithContext(WithoutScope(ctx)).Find(&remaining).Error; err != nil {
		t.Fatal(err)
	}
	if len(remaining) != 1 || remaining[0].TenantID != "globex" {
		t.Fatalf("globex 行应幸存, got %+v", remaining)
	}
}

// TestNormalizeAndKey 归一单出口与复合键形态。
func TestNormalizeAndKey(t *testing.T) {
	if got := Normalize("  "); got != Default {
		t.Fatalf("空白归一=%q, want %q", got, Default)
	}
	if got := Normalize(" acme "); got != "acme" {
		t.Fatalf("去空白归一=%q, want acme", got)
	}
	if got := Key("acme", "cache/x"); got != "acme/cache/x" {
		t.Fatalf("Key=%q", got)
	}
	if got := Key("", ""); got != Default {
		t.Fatalf("空 Key=%q, want %q", got, Default)
	}
}
