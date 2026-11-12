package gormx

import (
	"context"
	"testing"
)

// singletonCfgPK 主键列名非 "id" 的单例模型（回归测试用）。
type singletonCfgPK struct {
	UID   int64  `gorm:"primaryKey;column:uid;autoIncrement"`
	Name  string `gorm:"size:50"`
	Value string `gorm:"size:200"`
}

func (m *singletonCfgPK) GetID() int64   { return m.UID }
func (m *singletonCfgPK) SetID(id int64) { m.UID = id }

// TestSingletonRepo_PrimaryKeyColumnParsing 验证主键列名经 GORM schema 解析
// （回归测试：此前硬编码 Order("id DESC")，主键列名非 id 的模型查询报错）。
func TestSingletonRepo_PrimaryKeyColumnParsing(t *testing.T) {
	db := newTestDB(t, &singletonCfgPK{})
	repo := NewSingletonRepo(db, func() *singletonCfgPK {
		return &singletonCfgPK{Name: "default", Value: "v0"}
	})

	col, err := repo.primaryKeyColumn()
	if err != nil {
		t.Fatalf("primaryKeyColumn: %v", err)
	}
	if col != "uid" {
		t.Errorf("expected pk column 'uid', got %q", col)
	}

	// id 列名模型解析为 "id"
	db2 := newTestDB(t, &TestBase{})
	repo2 := NewSingletonRepo(db2, func() *TestBase { return &TestBase{Name: "d"} })
	col2, err := repo2.primaryKeyColumn()
	if err != nil {
		t.Fatalf("primaryKeyColumn(TestBase): %v", err)
	}
	if col2 != "id" {
		t.Errorf("expected pk column 'id', got %q", col2)
	}
}

// TestSingletonRepo_NonIDPrimaryKey_Cycle 回归测试：主键列名非 id 的模型
// Get/Save/GetOrCreate 完整可用（此前 Order("id DESC") 直接 SQL 报错）。
func TestSingletonRepo_NonIDPrimaryKey_Cycle(t *testing.T) {
	db := newTestDB(t, &singletonCfgPK{})
	repo := NewSingletonRepo(db, func() *singletonCfgPK {
		return &singletonCfgPK{Name: "cfg", Value: "default"}
	})
	ctx := context.Background()

	// 不存在 → 默认值
	got, err := repo.Get(ctx)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Value != "default" {
		t.Errorf("expected default value, got %q", got.Value)
	}

	// GetOrCreate 创建
	created, err := repo.GetOrCreate(ctx)
	if err != nil {
		t.Fatalf("GetOrCreate: %v", err)
	}
	if created.UID == 0 {
		t.Fatal("expected UID populated after GetOrCreate")
	}

	// Get 命中（Order by uid DESC 不报错）
	got, err = repo.Get(ctx)
	if err != nil {
		t.Fatalf("Get after create: %v", err)
	}
	if got.UID != created.UID || got.Value != "default" {
		t.Errorf("expected created record, got UID=%d Value=%q", got.UID, got.Value)
	}

	// Save 更新
	created.Value = "updated"
	if err = repo.Save(ctx, created); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err = repo.Get(ctx)
	if err != nil {
		t.Fatalf("Get after save: %v", err)
	}
	if got.Value != "updated" {
		t.Errorf("expected updated value, got %q", got.Value)
	}

	// GetOrCreate 命中已有记录（不重复创建）
	again, err := repo.GetOrCreate(ctx)
	if err != nil {
		t.Fatalf("GetOrCreate again: %v", err)
	}
	if again.UID != created.UID {
		t.Errorf("expected same record UID=%d, got %d", created.UID, again.UID)
	}
}

// TestSingletonRepo_IdColumnModel_StillWorks 验证 id 主键模型的存量行为不变。
func TestSingletonRepo_IdColumnModel_StillWorks(t *testing.T) {
	db := newTestDB(t, &TestBase{})
	repo := NewSingletonRepo(db, func() *TestBase { return &TestBase{Name: "d"} })
	ctx := context.Background()

	created, err := repo.GetOrCreate(ctx)
	if err != nil {
		t.Fatalf("GetOrCreate: %v", err)
	}
	if created.ID == 0 {
		t.Fatal("expected ID populated")
	}

	got, err := repo.Get(ctx)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.ID != created.ID {
		t.Errorf("expected ID %d, got %d", created.ID, got.ID)
	}
}

// TestNewSingletonRepo_NilDefaultValPanics 回归（gormx#182）：defaultVal 为
// nil 构造期即 panic（fail-fast，与仓内构造期校验纪律一致）——旧实现构造
// 成功，故障推迟到「线上空表首次 Get/GetOrCreate」才以 nil func deref panic
// 爆出，数据依赖的延迟故障出事点与根因点距离最远。
func TestNewSingletonRepo_NilDefaultValPanics(t *testing.T) {
	db := newTestDB(t, &singletonCfgPK{})
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("nil defaultVal must panic at construction (gormx#182)")
		}
	}()
	_ = NewSingletonRepo[*singletonCfgPK](db, nil)
}
