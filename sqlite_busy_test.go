package gormx

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// openBusyConn 打开一条静默日志的 sqlite 连接（busy 集成测试用）。
func openBusyConn(t *testing.T, dsn string) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatal(err)
	}
	return db
}

// ---- 单元：类型化路径 + 文本兜底（#77） ----

// fakeCoder 模拟 modernc.org/sqlite 的 *sqlite.Error 投影（Code() int）。
type fakeCoder struct {
	msg  string
	code int
}

func (e *fakeCoder) Error() string { return e.msg }
func (e *fakeCoder) Code() int     { return e.code }
func (e *fakeCoder) Unwrap() error { return nil }

func TestIsBusyErr_TypedPath(t *testing.T) {
	// modernc 真实形态：码 5/6 + 文案带 SQLITE_BUSY/SQLITE_LOCKED 标记
	if !IsBusyErr(&fakeCoder{msg: "The database file is locked (SQLITE_BUSY)", code: 5}) {
		t.Fatal("SQLITE_BUSY(5) 应判定为 busy")
	}
	if !IsBusyErr(&fakeCoder{msg: "A table in the database is locked (SQLITE_LOCKED)", code: 6}) {
		t.Fatal("SQLITE_LOCKED(6) 应判定为 busy")
	}
	// 错误链中间层：errors.As 应穿透 gorm/包装层
	wrapped := fmt.Errorf("gorm 层包装: %w", &fakeCoder{msg: "The database file is locked (SQLITE_BUSY)", code: 5})
	if !IsBusyErr(wrapped) {
		t.Fatal("errors 链应穿透包装层")
	}
	// 同名 Code() 但文案无标记 → 不误报（其他库的同名方法防线）
	if IsBusyErr(&fakeCoder{msg: "some other library error", code: 5}) {
		t.Fatal("码命中但文案无 SQLITE 标记不应误报")
	}
	// 非 busy 码
	if IsBusyErr(&fakeCoder{msg: "constraint (SQLITE_CONSTRAINT)", code: 19}) {
		t.Fatal("非 busy 码不应判定为 busy")
	}
}

func TestIsBusyErr_TextFallback(t *testing.T) {
	// 经典 mattn 文案（大小写不敏感）
	if !IsBusyErr(errors.New("database is locked")) {
		t.Fatal("经典文案应命中")
	}
	if !IsBusyErr(errors.New("Database Table Is Locked")) {
		t.Fatal("表锁文案应命中（大小写不敏感）")
	}
	if IsBusyErr(errors.New("no such table: foo")) {
		t.Fatal("普通错误不应命中")
	}
	if IsBusyErr(nil) {
		t.Fatal("nil 应返回 false")
	}
}

// ---- 集成：真 busy（两条连接写同一库，MATTN 文案时代的原始竞态） ----

// TestIsBusyErr_RealBusy 构造真实 SQLITE_BUSY：连接 A 持 IMMEDIATE 写锁，
// 连接 B 写入即撞 BUSY（宿主无法在单测里构造真 busy 是文本判定留下的根因，
// 本用例闭合「判定面与驱动真实行为等价」的验证）。
//
// 用**文件库**而非 memory+cache=shared：glebarez 驱动在共享缓存模式下对
// SQLITE_LOCKED_SHAREDCACHE 走 sqlite3_unlock_notify 内部无限等待（step 的
// retry 循环），锁等待被驱动吞掉、永远到不了调用方；文件库的跨连接写冲突
// 返回普通 SQLITE_BUSY，无 busy_timeout 时立返——这才是宿主真实撞到的形态。
func TestIsBusyErr_RealBusy(t *testing.T) {
	dsn := "file:" + t.TempDir() + "/busy.db"
	dbA := openBusyConn(t, dsn)
	dbB := openBusyConn(t, dsn)
	sqlA, _ := dbA.DB()
	sqlB, _ := dbB.DB()
	t.Cleanup(func() { _ = sqlA.Close(); _ = sqlB.Close() })

	// 连接数钳到 1：保证 A 的锁连接与 B 的写连接是不同连接，
	// 且 busy_timeout(0) 落在执行 INSERT 的同一条连接上（modernc 缺省
	// busy_timeout=5000ms 会让 B 白等 5s——测试要的是立返 BUSY）
	sqlA.SetMaxOpenConns(1)
	sqlB.SetMaxOpenConns(1)
	if err := dbB.Exec(`PRAGMA busy_timeout=0`).Error; err != nil {
		t.Fatalf("busy_timeout pragma: %v", err)
	}

	if err := dbA.Exec(`CREATE TABLE busy_probe (id INTEGER PRIMARY KEY, v TEXT)`).Error; err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := dbA.Exec(`INSERT INTO busy_probe (v) VALUES ('a')`).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}

	// A 持写事务不提交
	tx := dbA.Exec(`BEGIN IMMEDIATE`)
	if tx.Error != nil {
		t.Fatalf("begin immediate: %v", tx.Error)
	}
	defer func() { _ = dbA.Exec(`ROLLBACK`).Error }()

	// B 写入 → 应撞 busy；busy_timeout 未配置时立返
	done := make(chan error, 1)
	go func() {
		done <- dbB.Exec(`INSERT INTO busy_probe (v) VALUES ('b')`).Error
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("B 的写入应撞 busy（A 持 IMMEDIATE 锁未提交）")
		}
		if !IsBusyErr(err) {
			t.Fatalf("真实 busy 错误应被 IsBusyErr 识别，got: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("B 写入 5s 未返回——busy_timeout 未被置 0")
	}
}

// TestIsBusyErr_TextFallbackStillStable 文本兜底面对驱动文案漂移的防线价值：
// modernc 的 BUSY 文案不含经典 "database is locked" 子串——本库走类型化路径
// 拦住它（宿主旧文本匹配拦不住，这正是 #77 的立论）。
func TestIsBusyErr_TextFallbackStillStable(t *testing.T) {
	moderncBusy := "The database file is locked (SQLITE_BUSY)"
	if strings.Contains(moderncBusy, "database is locked") {
		t.Skip("modernc 文案已含经典子串，本用例失去意义")
	}
	if !IsBusyErr(&fakeCoder{msg: moderncBusy, code: 5}) {
		t.Fatal("类型化路径必须拦住 modernc busy（文本兜底拦不住它）")
	}
}
