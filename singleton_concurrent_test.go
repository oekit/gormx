package gormx

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// singletonFixedPK 主键取固定值的单例模型（#99 回归测试用）：
// defaultVal 带固定 uid=1，两个「并发首插」副本必然撞同一主键。
type singletonFixedPK struct {
	UID  int64  `gorm:"primaryKey;column:uid"`
	Name string `gorm:"size:50"`
}

func (m *singletonFixedPK) GetID() int64   { return m.UID }
func (m *singletonFixedPK) SetID(id int64) { m.UID = id }

// openSingletonDB 打开独立的 sqlite 内存库并注册 create 前置回调：
// 回调在**第一次** Create 时向同一事务注入一条 uid=1 的冲突行——随后被执行的
// Create 撞主键拿到 glebarez 驱动的裸文案唯一冲突（未开 TranslateError，错误
// **不映射** gorm.ErrDuplicatedKey）。注入行随事务回滚消失，重试轮的 Create
// 干净成功——这就是「败者重读收敛」的可控复现。
//
// 不用两个 goroutine 真并发：sqlite 回滚日志模式下「读后写」的两个事务会
// 互相持锁（SHARED/RESERVED 交叉），产出的是 BUSY 类错误而非唯一冲突，
// 测试会随调度抖动——回调注入形态是确定性的、无 sleep 的。
func openSingletonDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&singletonFixedPK{}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		sqlDB, _ := db.DB()
		_ = sqlDB.Close()
	})

	var injected atomic.Bool
	if err := db.Callback().Create().Before("gorm:create").Register("test:inject_conflict", func(tx *gorm.DB) {
		if _, isTarget := tx.Statement.Model.(*singletonFixedPK); !isTarget {
			return
		}
		if injected.Swap(true) {
			return // 只在第一轮 Create 注入；重试轮放行
		}
		if err := tx.Exec("INSERT INTO singleton_fixed_pks (uid, name) VALUES (1, 'other-party')").Error; err != nil {
			_ = tx.AddError(err)
		}
	}); err != nil {
		t.Fatal(err)
	}
	return db
}

// TestSingletonRepo_Save_ConcurrentFirstInsertConverges 回归（#99）：
// Create 撞唯一约束时 Save 必须重读改走更新路径收敛。旧判据只认
// gorm.ErrDuplicatedKey，sqlite（glebarez，未开 TranslateError）下唯一冲突
// 是裸字符串文案，收敛逻辑一次都不触发——错误直接返回给调用方。
func TestSingletonRepo_Save_ConcurrentFirstInsertConverges(t *testing.T) {
	db := openSingletonDB(t)
	repo := NewSingletonRepo(db, func() *singletonFixedPK {
		return &singletonFixedPK{UID: 1, Name: "default"}
	})

	if err := repo.Save(context.Background(), &singletonFixedPK{UID: 1, Name: "mine"}); err != nil {
		t.Fatalf("Save 应在唯一冲突后重试收敛，got: %v", err)
	}

	var got singletonFixedPK
	if err := db.First(&got, "uid = ?", 1).Error; err != nil {
		t.Fatalf("收敛后应存在 uid=1 的记录: %v", err)
	}
	if got.Name != "mine" {
		t.Errorf("expected Name=%q, got %q", "mine", got.Name)
	}
	var count int64
	db.Model(&singletonFixedPK{}).Count(&count)
	if count != 1 {
		t.Errorf("expected 1 row, got %d", count)
	}
}

// TestSingletonRepo_GetOrCreate_ConcurrentFirstInsertConverges 回归（#99）：
// GetOrCreate 同一收敛承诺；旧判据下同样的唯一冲突直接漏出。
func TestSingletonRepo_GetOrCreate_ConcurrentFirstInsertConverges(t *testing.T) {
	db := openSingletonDB(t)
	repo := NewSingletonRepo(db, func() *singletonFixedPK {
		return &singletonFixedPK{UID: 1, Name: "default"}
	})

	got, err := repo.GetOrCreate(context.Background())
	if err != nil {
		t.Fatalf("GetOrCreate 应在唯一冲突后重试收敛，got: %v", err)
	}
	if got.UID != 1 || got.Name != "default" {
		t.Errorf("expected uid=1 default record, got %+v", got)
	}
	var count int64
	db.Model(&singletonFixedPK{}).Count(&count)
	if count != 1 {
		t.Errorf("expected 1 row, got %d", count)
	}
}

// TestSingletonRepo_RetryCriterionIsIsUniqueViolation 判据单点验证：
// 收敛重试认的是 IsUniqueViolation（含驱动裸文案），不是只认
// gorm.ErrDuplicatedKey——后者的收敛承诺只在开了 TranslateError 的库上成立。
func TestSingletonRepo_RetryCriterionIsIsUniqueViolation(t *testing.T) {
	raw := errors.New("constraint failed: UNIQUE constraint failed: t.uid (2067)")
	if !IsUniqueViolation(raw) {
		t.Fatal("glebarez 裸文案应命中 IsUniqueViolation")
	}
	if errors.Is(raw, gorm.ErrDuplicatedKey) {
		t.Fatal("未开 TranslateError 时裸文案不应映射 ErrDuplicatedKey（前提失效即测试失效）")
	}
}
