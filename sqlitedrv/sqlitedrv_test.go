package sqlitedrv_test

import (
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/oekit/gormx"
	"github.com/oekit/gormx/sqlitedrv"
)

type acct struct {
	ID   uint   `gorm:"primarykey"`
	Name string `gorm:"uniqueIndex"`
}

// TestOpen_InMemory 入口冒烟：真开库、真迁移、真读写。
func TestOpen_InMemory(t *testing.T) {
	dsn := "file:" + uuid.New().String() + "?mode=memory&cache=shared"
	db, err := sqlitedrv.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if sqlDB, derr := db.DB(); derr == nil {
			_ = sqlDB.Close()
		}
	})
	if err := db.AutoMigrate(&acct{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&acct{Name: "a"}).Error; err != nil {
		t.Fatal(err)
	}
	var got acct
	if err := db.First(&got, "name = ?", "a").Error; err != nil {
		t.Fatal(err)
	}
}

// TestOpen_EmptyDSN 空 dsn 显式报错，不落到驱动层。
func TestOpen_EmptyDSN(t *testing.T) {
	if _, err := sqlitedrv.Open(""); err == nil {
		t.Fatal("空 dsn 应报错")
	}
}

// TestOpenPureGoSQLite_Alias 迁移别名与 Open 同行为（旧 gormx.OpenPureGoSQLite
// 调用点只改 import 路径即可迁移）。
func TestOpenPureGoSQLite_Alias(t *testing.T) {
	dsn := "file:" + uuid.New().String() + "?mode=memory&cache=shared"
	db, err := sqlitedrv.OpenPureGoSQLite(dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if sqlDB, derr := db.DB(); derr == nil {
			_ = sqlDB.Close()
		}
	})
	if err := db.AutoMigrate(&acct{}); err != nil {
		t.Fatal(err)
	}
}

// TestIsUniqueViolation_ThroughEntry 入口与判据同源的端到端验证：
// 经本入口打开的库撞唯一键，gormx.IsUniqueViolation 必须判得出
// （判据按纯 Go 驱动文案匹配，见 transaction.go）。
func TestIsUniqueViolation_ThroughEntry(t *testing.T) {
	dsn := "file:" + uuid.New().String() + "?mode=memory&cache=shared"
	db, err := sqlitedrv.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if sqlDB, derr := db.DB(); derr == nil {
			_ = sqlDB.Close()
		}
	})
	if err := db.AutoMigrate(&acct{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&acct{Name: "dup"}).Error; err != nil {
		t.Fatal(err)
	}
	dupErr := db.Create(&acct{Name: "dup"}).Error
	if dupErr == nil {
		t.Fatal("重复唯一键应报错")
	}
	if !gormx.IsUniqueViolation(dupErr) {
		t.Fatalf("IsUniqueViolation 未识别唯一冲突（入口与判据同源被破坏）: %v", dupErr)
	}
	// 负对照：普通错误不误判
	if gormx.IsUniqueViolation(errors.New("boom")) {
		t.Error("普通错误不应被判为唯一冲突")
	}
}
