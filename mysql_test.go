package gormx

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"strings"
	"testing"
	"time"

	gomysql "github.com/go-sql-driver/mysql"
	gormmysql "gorm.io/driver/mysql"
	"gorm.io/gorm"
	gormLogger "gorm.io/gorm/logger"
)

// #262 OpenMySQL 零校验零探活——本文件钉住构造期护栏：
//   - Host 空 / Port 越界构造期 fail-fast（redisx#217 同族口径：漏配 host
//     不是「改连本机」的暗号而是配置缺口，":3306" 经 Go dial 解析为回环，
//     本机恰有 MySQL 时数据写进错误实例且零日志）；
//   - DSN 经驱动自家的 Config.FormatDSN 组装：IPv6 经 net.JoinHostPort
//     括号化（裸 "%s:%d" 拼出 "tcp(::1:3306)" → "[::1:3306]:3306" 双层
//     括号错位）、用户/密码/库名走驱动格式化-解析往返契约；
//   - 构造后 Ping 探活失败回收连接池（redisx#149）。

// TestOpenMySQL_EmptyHostFailFast Host 为空构造期拒绝（#262）：校验发生在
// 组建 dialector 之前，错误即护栏文案而非 dial 报错。
func TestOpenMySQL_EmptyHostFailFast(t *testing.T) {
	_, err := OpenMySQL(MySQLConfig{Host: "", Port: 3306, User: "app", Password: "pw", DBName: "shop"})
	if err == nil {
		t.Fatal("Host 为空必须构造期 fail-fast（否则 :3306 静默连本机）")
	}
	if !strings.Contains(err.Error(), "host is empty") {
		t.Fatalf("错误应点名 host 为空, got: %v", err)
	}
}

// TestOpenMySQL_PortRangeFailFast Port 越界构造期拒绝（#262）：1..65535
// 之外（含零值缺省形态）都在拨号前拦截。
func TestOpenMySQL_PortRangeFailFast(t *testing.T) {
	for _, port := range []int{0, -1, 65536, 100000} {
		_, err := OpenMySQL(MySQLConfig{Host: "db.example.com", Port: port, User: "app", DBName: "shop"})
		if err == nil {
			t.Fatalf("Port=%d 必须构造期 fail-fast", port)
		}
		if !strings.Contains(err.Error(), "out of range 1..65535") {
			t.Fatalf("Port=%d 错误应点明合法区间, got: %v", port, err)
		}
	}
}

// TestBuildMySQLDSN_IPv6Bracketed IPv6 字面量经 net.JoinHostPort 正确括号
// 化（#262）：裸拼接的 "tcp(::1:3306)" 会被驱动兜成双层括号错位地址。
func TestBuildMySQLDSN_IPv6Bracketed(t *testing.T) {
	dsn, err := buildMySQLDSN(MySQLConfig{Host: "::1", Port: 3306, User: "app", Password: "pw", DBName: "shop"})
	if err != nil {
		t.Fatalf("buildMySQLDSN: %v", err)
	}
	if !strings.Contains(dsn, "tcp([::1]:3306)") {
		t.Fatalf("IPv6 host 必须括号化为 [::1]:3306, got: %q", dsn)
	}
	cfg, err := gomysql.ParseDSN(dsn)
	if err != nil {
		t.Fatalf("ParseDSN(回读): %v", err)
	}
	if cfg.Addr != "[::1]:3306" || cfg.Net != "tcp" {
		t.Fatalf("回读 addr = %q net = %q, want [::1]:3306 tcp", cfg.Addr, cfg.Net)
	}
}

// TestBuildMySQLDSN_PasswordRoundTrip 用户/密码/库名走驱动 FormatDSN→
// ParseDSN 往返契约（#262）：不再手工拼接、不依赖解析器反向扫描的实现
// 细节——含 @ : / ? & ( 空格 % = 的密码逐字符还原，地址不被挪位。
func TestBuildMySQLDSN_PasswordRoundTrip(t *testing.T) {
	for _, pw := range []string{
		"p@ss", "p:ss", "p/ss", "p?ss", "p&ss", "p(ss)", "p ss", "p%ss",
		"p=ss", "plain", "", "p@ss:with/slash?x&y",
	} {
		dsn, err := buildMySQLDSN(MySQLConfig{
			Host: "db.example.com", Port: 3306,
			User: "app", Password: pw, DBName: "shop",
		})
		if err != nil {
			t.Fatalf("password %q: buildMySQLDSN: %v", pw, err)
		}
		cfg, err := gomysql.ParseDSN(dsn)
		if err != nil {
			t.Fatalf("password %q: ParseDSN(%q): %v", pw, dsn, err)
		}
		if cfg.User != "app" || cfg.Passwd != pw || cfg.Addr != "db.example.com:3306" || cfg.DBName != "shop" {
			t.Fatalf("password %q 往返失真: user=%q passwd=%q addr=%q dbname=%q",
				pw, cfg.User, cfg.Passwd, cfg.Addr, cfg.DBName)
		}
	}
}

// TestBuildMySQLDSN_ParamsPreserved 连接参数与历史 DSN 逐项等价（#262）：
// charset=utf8mb4 / parseTime=true / loc=Local 一个不少。
func TestBuildMySQLDSN_ParamsPreserved(t *testing.T) {
	dsn, err := buildMySQLDSN(MySQLConfig{Host: "db.example.com", Port: 3306, User: "app", Password: "pw", DBName: "shop"})
	if err != nil {
		t.Fatalf("buildMySQLDSN: %v", err)
	}
	for _, want := range []string{"charset=utf8mb4", "parseTime=true", "loc=Local"} {
		if !strings.Contains(dsn, want) {
			t.Fatalf("DSN 缺参数 %s, got: %q", want, dsn)
		}
	}
	cfg, err := gomysql.ParseDSN(dsn)
	if err != nil {
		t.Fatalf("ParseDSN: %v", err)
	}
	if !cfg.ParseTime || cfg.Loc != time.Local {
		t.Fatalf("parseTime/loc 未还原: parseTime=%v loc=%v", cfg.ParseTime, cfg.Loc)
	}
}

// probeStubConn 桩连接：Ping 恒失败（driver.Pinger），供 probeConn 的
// 失败-回收路径无服务器验证。
type probeStubConn struct{}

func (probeStubConn) Prepare(query string) (driver.Stmt, error) {
	return nil, errors.New("probe stub: prepare not supported")
}
func (probeStubConn) Close() error { return nil }
func (probeStubConn) Begin() (driver.Tx, error) {
	return nil, errors.New("probe stub: tx not supported")
}
func (probeStubConn) Ping(ctx context.Context) error {
	return errors.New("probe stub: ping failed")
}

type probeStubConnector struct{}

func (probeStubConnector) Connect(context.Context) (driver.Conn, error) {
	return probeStubConn{}, nil
}
func (probeStubConnector) Driver() driver.Driver { return nil }

// newProbeStubDB 构造不触网、Ping 必失败的 gorm.DB（Conn 注入桩连接池 +
// SkipInitializeWithVersion 跳过版本查询 + DisableAutomaticPing 跳过
// gorm.Open 自身的 Ping）。
func newProbeStubDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(gormmysql.New(gormmysql.Config{
		Conn:                      sql.OpenDB(probeStubConnector{}),
		SkipInitializeWithVersion: true,
	}), &gorm.Config{
		Logger:               gormLogger.Default.LogMode(gormLogger.Silent),
		DisableAutomaticPing: true,
	})
	if err != nil {
		t.Fatalf("open probe stub db: %v", err)
	}
	return db
}

// TestProbeConn_FailureClosesPool 探活失败必须回收连接池（#262，对齐
// redisx#149）：probeConn 返回点名 ping 的错误；回收之后同一连接池再
// Ping 报「database is closed」——证明 Close 真的执行了，不会带着死实例
// 静默放行。
func TestProbeConn_FailureClosesPool(t *testing.T) {
	db := newProbeStubDB(t)
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("db.DB(): %v", err)
	}

	err = probeConn(db)
	if err == nil {
		t.Fatal("桩连接 Ping 必失败，probeConn 必须报错")
	}
	if !strings.Contains(err.Error(), "ping failed") {
		t.Fatalf("错误应点名 ping 失败, got: %v", err)
	}

	// 回收证明：池已 Close ⇒ 再 Ping 报 database is closed
	err = sqlDB.PingContext(context.Background())
	if err == nil || !strings.Contains(err.Error(), "database is closed") {
		t.Fatalf("探活失败后连接池必须被回收（再 Ping 应报 database is closed）, got: %v", err)
	}
}

// TestWithSkipProbe_OptionWiring WithSkipProbe 开关注入 openConfig（#262）。
func TestWithSkipProbe_OptionWiring(t *testing.T) {
	oc := &openConfig{}
	WithSkipProbe()(oc)
	if !oc.skipProbe {
		t.Fatal("WithSkipProbe 必须置位 skipProbe")
	}
}
