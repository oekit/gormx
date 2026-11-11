package migrate

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/oekit/gormx"
)

// TestRunContextCancelledFailsBeforeDDL（#177 回归）：ctx 已取消时
// RunContext 必须显式报错且**零 DDL 执行**——此前全链路无 ctx（Run 用
// context.Background()、语句 d.Exec 不带 WithContext），部署脚本一旦卡住
// 只能杀进程；现 ctx 贯穿 d.WithContext(ctx).Exec，取消即中断。
func TestRunContextCancelledFailsBeforeDDL(t *testing.T) {
	db := openMigrateTestDB(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	opts := Options{
		Target:  "mysql",
		Install: []string{"01-install.sql"},
		FS:      testFS(),
		Root:    "ddl",
	}
	err := RunContext(ctx, db, opts)
	if err == nil {
		t.Fatal("ctx 已取消时 RunContext 必须报错")
	}
	if !strings.Contains(err.Error(), context.Canceled.Error()) {
		t.Fatalf("错误应携带 ctx 取消原因, got: %v", err)
	}
	// 取消发生在任何语句执行前 ⇒ t_a 不得被建出
	var rows int
	if qerr := db.Raw("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='t_a'").Scan(&rows).Error; qerr != nil {
		t.Fatalf("probe sqlite_master: %v", qerr)
	}
	if rows != 0 {
		t.Error("ctx 已取消时不应执行任何 DDL（t_a 不应存在）")
	}
}

// TestRunContextExecutesScripts 正向面：ctx 存活时行为与 Run 一致。
func TestRunContextExecutesScripts(t *testing.T) {
	db := openMigrateTestDB(t)
	opts := Options{
		Target:  "mysql",
		Install: []string{"01-install.sql"},
		Upgrade: []string{"02-upgrade.sql"},
		FS:      testFS(),
		Root:    "ddl",
	}
	if err := RunContext(context.Background(), db, opts); err != nil {
		t.Fatalf("RunContext() error = %v", err)
	}
	var rows int
	if err := db.Raw("SELECT COUNT(*) FROM t_a").Scan(&rows).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if rows != 2 {
		t.Fatalf("t_a rows = %d, want 2", rows)
	}
}

// TestRunIsRunContextWithBackground（#177 兼容口径）：旧 Run 入口 =
// RunContext(Background) 薄包装，行为等价（既有 Run 用例继续覆盖兼容面，
// 本用例钉住两者同链路）。
func TestRunIsRunContextWithBackground(t *testing.T) {
	db := openMigrateTestDB(t)
	opts := Options{
		Target:  "mysql",
		Install: []string{"missing.sql"},
		FS:      testFS(),
		Root:    "ddl",
	}
	// 同一缺脚本场景：Run 与 RunContext 必须同样显式报错
	if err := Run(db, opts); err == nil {
		t.Fatal("Run() with missing script must fail")
	}
	db2 := openMigrateTestDB(t)
	if err := RunContext(context.Background(), db2, opts); err == nil {
		t.Fatal("RunContext(Background) with missing script must fail")
	}
}

// TestRunContextLockKeyUnsupportedDialect LockKey 路径同样走 ctx 链路：
// sqlite 不支持咨询锁 → 显式 ErrAdvisoryLockUnsupported（fail-closed，
// 与 Run 的既定方言策略一致）。
func TestRunContextLockKeyUnsupportedDialect(t *testing.T) {
	db := openMigrateTestDB(t)
	opts := Options{
		Target:  "mysql",
		Install: []string{"01-install.sql"},
		FS:      testFS(),
		Root:    "ddl",
		LockKey: "mydb+ddl-manifest",
	}
	err := RunContext(context.Background(), db, opts)
	if !errors.Is(err, gormx.ErrAdvisoryLockUnsupported) {
		t.Fatalf("sqlite + LockKey 应报 ErrAdvisoryLockUnsupported, got: %v", err)
	}
}

// TestSplitTopLevelClausesBackslashEscape（#175 回归）：migrate 的顶层逗号
// 切分换用 internal/sqltext 正典版后获得反斜杠逃逸能力——含转义引号的
// 子句（DEFAULT 'it\'s' 等）不再把逃逸后的引号误判为闭引号、把子句内的
// 逗号误切。单引号 DEFAULT 值里的顶层逗号必须留在同一条子句里。
func TestSplitTopLevelClausesBackslashEscape(t *testing.T) {
	got := splitTopLevelClauses(`ADD COLUMN a VARCHAR(10) DEFAULT 'it\'s,x', ADD COLUMN b INT`)
	if len(got) != 2 {
		t.Fatalf("逃逸引号内的逗号不得切分, got %#v", got)
	}
	if got[0] != `ADD COLUMN a VARCHAR(10) DEFAULT 'it\'s,x'` {
		t.Errorf("clauses[0] = %q, want %q", got[0], `ADD COLUMN a VARCHAR(10) DEFAULT 'it\'s,x'`)
	}
	if got[1] != "ADD COLUMN b INT" {
		t.Errorf("clauses[1] = %q, want %q", got[1], "ADD COLUMN b INT")
	}
}
