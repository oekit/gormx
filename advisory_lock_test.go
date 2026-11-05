package gormx

import (
	"context"
	"errors"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// TestAdvisoryLockSQLs_DialectDispatch 分发纯函数：两支持方言 + 未知方言哨兵。
func TestAdvisoryLockSQLs_DialectDispatch(t *testing.T) {
	lock, unlock, err := advisoryLockSQLs("postgres")
	if err != nil {
		t.Fatal(err)
	}
	if lock != "SELECT pg_advisory_lock(?)" || unlock != "SELECT pg_advisory_unlock(?)" {
		t.Fatalf("pg sqls: %q / %q", lock, unlock)
	}

	lock, unlock, err = advisoryLockSQLs("mysql")
	if err != nil {
		t.Fatal(err)
	}
	if lock != "SELECT GET_LOCK(?, -1)" || unlock != "SELECT RELEASE_LOCK(?)" {
		t.Fatalf("mysql sqls: %q / %q", lock, unlock)
	}

	_, _, err = advisoryLockSQLs("sqlite")
	if !errors.Is(err, ErrAdvisoryLockUnsupported) {
		t.Fatalf("sqlite 应返回 ErrAdvisoryLockUnsupported, got %v", err)
	}
}

// TestRunWithAdvisoryLock_UnsupportedDialect sqlite 上显式失败——互斥诉求在
// 不支持方言上必须报错而不是静默无锁执行。
func TestRunWithAdvisoryLock_UnsupportedDialect(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	called := false
	err = RunWithAdvisoryLock(context.Background(), db, 42, func(tx *gorm.DB) error {
		called = true
		return nil
	})
	if !errors.Is(err, ErrAdvisoryLockUnsupported) {
		t.Fatalf("want ErrAdvisoryLockUnsupported, got %v", err)
	}
	if called {
		t.Fatal("fn must not run when lock is unsupported")
	}
}

// TestRunWithAdvisoryLock_NilGuards 空参守卫。
func TestRunWithAdvisoryLock_NilGuards(t *testing.T) {
	if err := RunWithAdvisoryLock(context.Background(), nil, 1, func(tx *gorm.DB) error { return nil }); err == nil {
		t.Fatal("nil db must be rejected")
	}
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := RunWithAdvisoryLock(context.Background(), db, 1, nil); err == nil {
		t.Fatal("nil fn must be rejected")
	}
}
