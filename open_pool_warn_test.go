package gormx

import (
	"bytes"
	"log"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/google/uuid"
)

// captureStdLog 把标准库 log 的输出重定向到 buffer（logx 缺省后端经标准库
// log 输出，gormLog 是 Delegating 懒解析 ⇒ WARN 文案可被捕获断言）。
func captureStdLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	buf := &bytes.Buffer{}
	prev := log.Writer()
	log.SetOutput(buf)
	t.Cleanup(func() { log.SetOutput(prev) })
	return buf
}

func openSQLite(t *testing.T, opts ...OpenOption) {
	t.Helper()
	dialector := sqlite.Open("file:" + uuid.New().String() + "?mode=memory&cache=shared")
	db, err := Open(dialector, opts...)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	sqlDB, _ := db.DB()
	t.Cleanup(func() { _ = sqlDB.Close() })
}

// TestOpen_PoolWarn_NotConfigured 未传 option：保持 #75 原文案——
// 指引排查者去调用代码补显式 gormx.WithConnectionPool。
func TestOpen_PoolWarn_NotConfigured(t *testing.T) {
	buf := captureStdLog(t)
	openSQLite(t)
	if !strings.Contains(buf.String(), "连接池未设置 MaxOpenConns 上限") ||
		!strings.Contains(buf.String(), "建议显式 gormx.WithConnectionPool") {
		t.Errorf("未传 option 应打「未设置上限」WARN, got: %q", buf.String())
	}
	if strings.Contains(buf.String(), "请检查配置项来源") {
		t.Errorf("未传 option 不应误导排查者去查配置项来源, got: %q", buf.String())
	}
}

// TestOpen_PoolWarn_ExplicitZero 显式传 0（#97，kuboard-go 实录形态：env
// 未设时三个值全 0 传入）：文案必须指向「配置项来源」，而不是「去显式配置」
// ——调用点恰恰已经显式配置了。
func TestOpen_PoolWarn_ExplicitZero(t *testing.T) {
	buf := captureStdLog(t)
	openSQLite(t, WithConnectionPool(0, 0, time.Hour))
	if !strings.Contains(buf.String(), "连接池显式配置为 MaxOpenConns=0") ||
		!strings.Contains(buf.String(), "请检查配置项来源") {
		t.Errorf("显式传 0 应打「显式配置为 0」WARN, got: %q", buf.String())
	}
	if strings.Contains(buf.String(), "建议显式 gormx.WithConnectionPool") {
		t.Errorf("显式传 0 时指引「去显式配置」与事实相反（调用点已配置）, got: %q", buf.String())
	}
}

// TestOpen_PoolWarn_ConfiguredNoWarn 正常配置上限：无 WARN。
func TestOpen_PoolWarn_ConfiguredNoWarn(t *testing.T) {
	buf := captureStdLog(t)
	openSQLite(t, WithConnectionPool(5, 2, time.Hour))
	if strings.Contains(buf.String(), "MaxOpenConns") {
		t.Errorf("已配置上限不应打 WARN, got: %q", buf.String())
	}
}

// TestOpen_PoolWarn_OnlyIdleTimeConfigured 只传 WithConnMaxIdleTime：
// 没有触及 MaxOpenConns 的设置面 ⇒ 按「未设置」形态警告（与 #75 口径一致）。
func TestOpen_PoolWarn_OnlyIdleTimeConfigured(t *testing.T) {
	buf := captureStdLog(t)
	openSQLite(t, WithConnMaxIdleTime(5*time.Minute))
	if !strings.Contains(buf.String(), "连接池未设置 MaxOpenConns 上限") {
		t.Errorf("只配 idle time 应按「未设置」形态 WARN, got: %q", buf.String())
	}
	if strings.Contains(buf.String(), "请检查配置项来源") {
		t.Errorf("未显式设置 MaxOpenConns 却按「配置项来源」指引，形态判反了: %q", buf.String())
	}
}
