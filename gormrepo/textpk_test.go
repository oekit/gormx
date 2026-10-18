package gormrepo

import (
	"context"
	"errors"
	"testing"

	"github.com/oekit/gormx/crud"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// docEntity 文本主键实体（TEXT PRIMARY KEY，客户端生成 hex 串）——issue #7 场景。
type docEntity struct {
	ID   string `gorm:"primaryKey;size:32"`
	Name string `gorm:"size:50"`
}

const (
	textPKID  = "a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6"
	textPKID2 = "ffffffffffffffffffffffffffffffaa"
)

func setupTextPKDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&docEntity{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

// TestGormRepository_TextPK_CRUD 钉住文本主键全路径：按 ID 路径必须经
// clause.PrimaryColumn（schema 解析 + 参数化）——GORM 对内联 string 参数
// 按 SQL 条件片段解析（非数字即注入面/语法错误），文本主键走内联必炸。
func TestGormRepository_TextPK_CRUD(t *testing.T) {
	db := setupTextPKDB(t)
	ctx := context.Background()
	repo := NewGormRepository[docEntity, string](db)

	if err := repo.Create(ctx, &docEntity{ID: textPKID, Name: "doc1"}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := repo.GetByID(ctx, textPKID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.Name != "doc1" {
		t.Errorf("Name = %q, want doc1", got.Name)
	}

	if _, err := repo.GetByID(ctx, textPKID2); !errors.Is(err, crud.ErrNotFound) {
		t.Errorf("miss want ErrNotFound chain, got %v", err)
	}

	if err := repo.UpdateFields(ctx, textPKID, map[string]any{"name": "doc1-upd"}); err != nil {
		t.Fatalf("UpdateFields: %v", err)
	}
	got, _ = repo.GetByID(ctx, textPKID)
	if got.Name != "doc1-upd" {
		t.Errorf("after UpdateFields Name = %q, want doc1-upd", got.Name)
	}

	if err := repo.Delete(ctx, textPKID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := repo.GetByID(ctx, textPKID); !errors.Is(err, crud.ErrNotFound) {
		t.Errorf("after Delete want ErrNotFound, got %v", err)
	}
}

// TestGormRepository_TextPK_BatchDelete 批量删除按 string 主键。
func TestGormRepository_TextPK_BatchDelete(t *testing.T) {
	db := setupTextPKDB(t)
	ctx := context.Background()
	repo := NewGormRepository[docEntity, string](db)

	for _, id := range []string{textPKID, textPKID2} {
		if err := repo.Create(ctx, &docEntity{ID: id, Name: "doc-" + id[:4]}); err != nil {
			t.Fatalf("Create %s: %v", id, err)
		}
	}
	if err := repo.BatchDelete(ctx, []string{textPKID, textPKID2}); err != nil {
		t.Fatalf("BatchDelete: %v", err)
	}
	var count int64
	if err := db.Model(&docEntity{}).Count(&count).Error; err != nil {
		t.Fatalf("Count: %v", err)
	}
	if count != 0 {
		t.Errorf("after BatchDelete count = %d, want 0", count)
	}
}

// TestBaseService_TextPK_ServicePath 文本主键走 BaseService 全链路
// （含 TxRepository 事务路径：sqlite 单写者天然串行，行锁读退化为普通读）。
func TestBaseService_TextPK_ServicePath(t *testing.T) {
	db := setupTextPKDB(t)
	ctx := context.Background()
	repo := NewGormRepository[docEntity, string](db)
	svc := crud.NewBaseService[docEntity, string, map[string]any, map[string]any](repo).
		SetToEntity(func(r *map[string]any) *docEntity {
			return &docEntity{ID: (*r)["id"].(string), Name: (*r)["name"].(string)}
		}).
		SetUpdateEntity(func(e *docEntity, r *map[string]any) { e.Name = (*r)["name"].(string) })

	req := map[string]any{"id": textPKID, "name": "doc1"}
	if _, err := svc.Create(ctx, &req); err != nil {
		t.Fatalf("Create: %v", err)
	}

	updReq := map[string]any{"name": "doc1-tx"}
	updated, err := svc.Update(ctx, textPKID, &updReq)
	if err != nil {
		t.Fatalf("Update (tx path): %v", err)
	}
	if updated.Name != "doc1-tx" {
		t.Errorf("updated Name = %q, want doc1-tx", updated.Name)
	}

	if _, err := svc.Get(ctx, textPKID); err != nil {
		t.Fatalf("Get: %v", err)
	}

	if err := svc.Delete(ctx, textPKID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := svc.Get(ctx, textPKID); !errors.Is(err, crud.ErrNotFound) {
		// Service.Get 把 ErrNotFound 包装为 404 BizError，errors.Is 链可达
		t.Errorf("after Delete want 404 chain, got %v", err)
	}
}
