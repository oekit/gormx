package gormx

import (
	"context"
	"strings"
	"sync"
	"testing"

	"gorm.io/gorm"
)

// TestAuditPlugin_BatchCreate_SetsAuditFields 回归测试：BatchCreate（slice
// ReflectValue）时审计字段必须逐元素填充 —— 此前 setField 遇到 slice 直接跳过，
// 批量插入静默得到空 created_by/updated_by。
func TestAuditPlugin_BatchCreate_SetsAuditFields(t *testing.T) {
	db := newTestDB(t, &TestAudit{})
	if err := db.Use(NewAuditPlugin(func(ctx context.Context) int64 {
		return 42
	})); err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&TestAudit{}); err != nil {
		t.Fatal(err)
	}

	users := []*TestAudit{
		{Name: "b1"},
		{Name: "b2"},
		{Name: "b3"},
	}
	if err := db.WithContext(context.Background()).CreateInBatches(&users, 2).Error; err != nil {
		t.Fatalf("CreateInBatches: %v", err)
	}

	for i, u := range users {
		if u.CreatedBy != 42 {
			t.Errorf("user %d: expected CreatedBy=42, got %d", i, u.CreatedBy)
		}
	}

	// 从 DB 侧二次确认（而非仅内存对象）
	var loaded []TestAudit
	if err := db.WithContext(context.Background()).Find(&loaded).Error; err != nil {
		t.Fatalf("Find: %v", err)
	}
	if len(loaded) != 3 {
		t.Fatalf("expected 3 rows, got %d", len(loaded))
	}
	for i, u := range loaded {
		if u.CreatedBy != 42 {
			t.Errorf("loaded user %d: expected CreatedBy=42, got %d", i, u.CreatedBy)
		}
	}
}

// TestAuditPlugin_BatchCreate_NonPointerSlice 验证值类型 slice（[]TestAudit）
// 的批量插入同样填充审计字段。
func TestAuditPlugin_BatchCreate_NonPointerSlice(t *testing.T) {
	db := newTestDB(t, &TestAudit{})
	if err := db.Use(NewAuditPlugin(func(ctx context.Context) int64 {
		return 7
	})); err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&TestAudit{}); err != nil {
		t.Fatal(err)
	}

	users := []TestAudit{{Name: "v1"}, {Name: "v2"}}
	if err := db.WithContext(context.Background()).Create(&users).Error; err != nil {
		t.Fatalf("Create(slice): %v", err)
	}

	for i, u := range users {
		if u.CreatedBy != 7 {
			t.Errorf("user %d: expected CreatedBy=7, got %d", i, u.CreatedBy)
		}
	}
}

// TestAuditPlugin_MapUpdates_SetsUpdatedBy 回归：setField 曾只改
// ReflectValue（struct），Updates(map[string]any) 的 SET 列表完全来自 map
// ——该路径 updated_by 恒为 0 且无告警。钉住：map 路径按 schema 列名注入。
func TestAuditPlugin_MapUpdates_SetsUpdatedBy(t *testing.T) {
	db := newTestDB(t, &TestAudit{})
	if err := db.Use(NewAuditPlugin(func(ctx context.Context) int64 {
		return 99
	})); err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&TestAudit{}); err != nil {
		t.Fatal(err)
	}

	u := &TestAudit{Name: "mu"}
	if err := db.WithContext(context.Background()).Create(u).Error; err != nil {
		t.Fatal(err)
	}

	// map 形式的 Updates（crud UpdateFields 走的就是这条路）
	if err := db.Model(&TestAudit{}).Where("id = ?", u.ID).
		Updates(map[string]any{"name": "mu2"}).Error; err != nil {
		t.Fatal(err)
	}

	var loaded TestAudit
	if err := db.WithContext(context.Background()).First(&loaded, u.ID).Error; err != nil {
		t.Fatal(err)
	}
	if loaded.UpdatedBy != 99 {
		t.Errorf("map Updates should fill updated_by=99, got %d", loaded.UpdatedBy)
	}
}

// TestAuditPlugin_MapUpdates_DoesNotMutateCallerMap 回归（#268）：
// GORM 的 Updates(map) 把调用方 map 原样赋给 Statement.Dest，插件直接
// dest[updated_by]=value 会污染调用方缓存 map——顺序复用时上次注入的值
// 残留（下次 UserIDFunc 返回 0 的那次仍把旧 updated_by 落库）。修复后
// 插件写 maps.Clone 副本，调用方 map 保持原样。
func TestAuditPlugin_MapUpdates_DoesNotMutateCallerMap(t *testing.T) {
	db := newTestDB(t)
	if err := db.Use(NewAuditPlugin(func(context.Context) int64 {
		return 99
	})); err != nil {
		t.Fatal(err)
	}

	fields := map[string]any{"name": "mu"}
	tx := db.Session(&gorm.Session{DryRun: true}).
		Model(&TestAudit{}).Where("id = ?", 1).Updates(fields)
	if tx.Error != nil {
		t.Fatalf("dry-run Updates: %v", tx.Error)
	}
	if !strings.Contains(tx.Statement.SQL.String(), "updated_by") {
		t.Fatalf("插件未执行（DryRun SQL 不含 updated_by）：%s", tx.Statement.SQL.String())
	}
	if v, ok := fields["updated_by"]; ok {
		t.Fatalf("插件写入了调用方 map：updated_by=%v（复用会残留上次注入值）", v)
	}
	if len(fields) != 1 || fields["name"] != "mu" {
		t.Fatalf("调用方 map 被改动：%v", fields)
	}
}

// TestAuditPlugin_MapUpdates_ConcurrentSharedMap 回归（#268，配合 -race）：
// 调用方复用同一个 fields map 并发调用 Updates(map)（gormrepo.UpdateFields
// 的路径）时，插件直接写调用方 map 会触发进程级 fatal error:
// concurrent map writes。修复后每次注入写独立副本，共享 map 只读。
func TestAuditPlugin_MapUpdates_ConcurrentSharedMap(t *testing.T) {
	db := newTestDB(t)
	if err := db.Use(NewAuditPlugin(func(context.Context) int64 {
		return 99
	})); err != nil {
		t.Fatal(err)
	}

	fields := map[string]any{"name": "mu"}
	const workers = 32
	var wg sync.WaitGroup
	start := make(chan struct{})
	errs := make(chan error, workers)
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			// DryRun：只走语句构建与 beforeUpdate 插件路径，不落库；
			// GORM 的 *gorm.DB 链式方法每次派生独立语句，可并发使用。
			errs <- db.Session(&gorm.Session{DryRun: true}).
				Model(&TestAudit{}).Where("id = ?", 1).Updates(fields).Error
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Errorf("并发 Updates: %v", err)
		}
	}
	if _, ok := fields["updated_by"]; ok {
		t.Error("插件修改了调用方共享 map")
	}
}

// TestAuditPlugin_SelectWhitelist_KeepsUpdatedBy 回归（#268）：
// 列白名单（db.Select(cols) 使 Statement.Selects 受限）下 struct 更新的
// SET 只含白名单命中列——插件改了实体 UpdatedBy 也会被静默剔除，审计链
// 出现系统性空洞且无日志。修复后插件把审计列追加进 Selects，updated_by
// 必须落库。
func TestAuditPlugin_SelectWhitelist_KeepsUpdatedBy(t *testing.T) {
	db := newTestDB(t, &TestAudit{})
	if err := db.Use(NewAuditPlugin(func(context.Context) int64 {
		return 99
	})); err != nil {
		t.Fatal(err)
	}

	u := &TestAudit{Name: "sw"}
	if err := db.WithContext(context.Background()).Create(u).Error; err != nil {
		t.Fatal(err)
	}

	patch := &TestAudit{Name: "sw2"}
	patch.ID = u.ID
	if err := db.WithContext(context.Background()).Model(patch).
		Select("name").Updates(patch).Error; err != nil {
		t.Fatalf("whitelist Updates: %v", err)
	}

	var loaded TestAudit
	if err := db.WithContext(context.Background()).First(&loaded, u.ID).Error; err != nil {
		t.Fatal(err)
	}
	if loaded.Name != "sw2" {
		t.Errorf("白名单内 name 应更新，got %q", loaded.Name)
	}
	if loaded.UpdatedBy != 99 {
		t.Errorf("Select 白名单把审计列静默剔除：updated_by=%d, want 99", loaded.UpdatedBy)
	}
}
