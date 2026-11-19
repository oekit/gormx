package gormx

import (
	"context"
	"errors"
	"net"
	"strconv"
	"time"

	gomysql "github.com/go-sql-driver/mysql"
	pkgerrors "github.com/oekit/pkg/errors"
	gormmysql "gorm.io/driver/mysql"
	"gorm.io/gorm"
)

// MySQLConfig 是 MySQL 连接配置，可直接从 YAML 反序列化。
type MySQLConfig struct {
	Host           string `yaml:"host"`
	Port           int    `yaml:"port"`
	User           string `yaml:"user"`
	Password       string `yaml:"password"`
	DBName         string `yaml:"dbname"`
	MaxIdleConns   int    `yaml:"max_idle_conns"`
	MaxOpenConns   int    `yaml:"max_open_conns"`
	ConnMaxLifeMin int    `yaml:"conn_max_life_min"`
}

// probeTimeout 构造期连通性验证的超时（#262）：探测类 ≤10s（AGENTS.md
// 超时纪律）——Ping 卡死比失败更糟，必须把「构造期等死」圈在有界窗口内。
const probeTimeout = 10 * time.Second

// OpenMySQL 从配置创建 GORM DB 实例（含连接池配置与构造期连通性验证；零
// tracing 注入，需要 SQL Span 经 github.com/oekit/tracing/gormotel 显式挂载）。
//
// 构造期护栏（#262，对齐 redisx config.go 的 redisx#217/#149 同族口径）：
//   - Host 为空、Port 不在 1..65535 构造期 fail-fast 返回 error，不发起
//     任何拨号——漏配 host 不是错误而是「改连本机」（":3306" 经 Go dial
//     解析为回环，redisx#217 同款事故面）：本机恰有 MySQL 时构造「成功」、
//     数据写进错误实例且零日志，gormx 对齐同口径显式报错拒绝；
//   - DSN 经 go-sql-driver 自家的 Config.FormatDSN 组装（Addr 用
//     net.JoinHostPort：Host 为 IPv6 字面量（如 "::1"）时正确括号化为
//     "[::1]:3306"——裸 "%s:%d" 会拼出 "tcp(::1:3306)"，驱动兜进
//     ensureHavePort 后成了 "[::1:3306]:3306" 双层括号错位地址，错误停在
//     dial 期且不点名 host）；密码/用户/库名交由驱动自身的往返契约
//     （FormatDSN→ParseDSN），不再手工拼接；
//   - 打开后立即 Ping 验证连通性：失败回收连接池（Close），回收错误与
//     Ping 错误经 errors.Join 聚合上抛（redisx#149：连接已拨号而 Ping
//     失败是唯一能观测到连接回收失败的时机，不得静默丢弃）。测试 / 离线
//     场景可用 WithSkipProbe 跳过该验证。
//
// 用法：
//
//	db, err := gormx.OpenMySQL(gormx.MySQLConfig{
//	    Host: cfg.Database.Host, Port: cfg.Database.Port,
//	    User: cfg.Database.User, Password: cfg.Database.Password,
//	    DBName: cfg.Database.DBName,
//	    MaxIdleConns: 10, MaxOpenConns: 100,
//	})
func OpenMySQL(cfg MySQLConfig, opts ...OpenOption) (*gorm.DB, error) {
	// 构造期 fail-fast（#262）：配置缺口在拨号前拒绝（redisx#217 同族口径
	// ——「漏配 host 不是错误而是改连本机」，改连本机恰是事故面本身）。
	if cfg.Host == "" {
		return nil, pkgerrors.New("gormx: mysql host is empty (config key host)——漏配 host 不是「改连本机」的暗号而是配置缺口（\":3306\" 经 Go dial 解析为回环），本机恰有 MySQL 时构造成功、数据写进错误实例且零日志")
	}
	if cfg.Port < 1 || cfg.Port > 65535 {
		return nil, pkgerrors.Newf("gormx: mysql port %d out of range 1..65535 (config key port)", cfg.Port)
	}

	dsn, err := buildMySQLDSN(cfg)
	if err != nil {
		return nil, err
	}

	if cfg.MaxIdleConns <= 0 {
		cfg.MaxIdleConns = 10
	}
	if cfg.MaxOpenConns <= 0 {
		cfg.MaxOpenConns = 100
	}

	// ConnMaxLifetime 生产默认 30 分钟（防火墙/NAT 空闲连接被静默掐断后，
	// 不限期的长连接会引发 "invalid connection" 突发错误——周期重建是
	// database/sql 的标准实践）。显式传负值 = 真正不限制。
	connMaxLife := time.Duration(cfg.ConnMaxLifeMin) * time.Minute
	if connMaxLife == 0 {
		connMaxLife = 30 * time.Minute
	}

	// 池默认值在前、调用方 opts 在后：显式传入的 OpenOption 可覆盖默认池
	// 配置（选项次序 = 后写者生效）。#76：maxOpen 在前。
	opts = append([]OpenOption{WithConnectionPool(cfg.MaxOpenConns, cfg.MaxIdleConns, connMaxLife)}, opts...)

	db, err := Open(gormmysql.Open(dsn), opts...)
	if err != nil {
		return nil, err
	}

	// WithSkipProbe 经同一 openConfig 消费：预扫一遍 opts 只为取开关
	// （选项都是幂等字段赋值，两次应用互不影响）。
	oc := &openConfig{}
	for _, opt := range opts {
		opt(oc)
	}
	if oc.skipProbe {
		return db, nil
	}
	if err := probeConn(db); err != nil {
		return nil, err
	}
	return db, nil
}

// buildMySQLDSN 用 go-sql-driver 自家的 Config.FormatDSN 组装 DSN（#262）。
//
// 此前手工 Sprintf 依赖「读驱动源码才能确认」的隐式容忍：密码含 @/: 等靠
// ParseDSN 的反向扫描恰好不越界（实测能往返，但这是解析器实现细节而非
// 契约）；Host 为 IPv6 字面量则必坏（见 OpenMySQL 注释）。FormatDSN 是
// 驱动的规范出口：Addr 经 net.JoinHostPort 括号化、DBName 经 url.PathEscape、
// 用户/密码/库名走驱动自身的格式化-解析往返契约。连接参数与历史 DSN 逐项
// 等价（charset=utf8mb4&parseTime=True&loc=Local）。
func buildMySQLDSN(cfg MySQLConfig) (string, error) {
	mc := gomysql.NewConfig()
	mc.User = cfg.User
	mc.Passwd = cfg.Password
	mc.Net = "tcp"
	mc.Addr = net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port))
	mc.DBName = cfg.DBName
	// Charset 选项经 Apply 注入（v1.10 起返回 error，禁 `_` 吞——常量实参
	// 下确定性成功，但错误处理纪律要求显式接住）。
	if err := mc.Apply(gomysql.Charset("utf8mb4", "")); err != nil {
		return "", pkgerrors.Wrapf(err, "gormx: build mysql dsn charset")
	}
	mc.ParseTime = true
	mc.Loc = time.Local
	return mc.FormatDSN(), nil
}

// probeConn 构造期连通性验证（#262，对齐 redisx#149）：Ping 失败时回收
// 已创建的连接池（连接可能已拨号），回收失败与 Ping 失败经 errors.Join
// 聚合上抛——两侧各自 %w 包装，errors.Is/As 判定口径不变；回收成功时
// 返回口径与历史一致（仅 Ping 错误）。
func probeConn(db *gorm.DB) error {
	sqlDB, err := db.DB()
	if err != nil {
		return err
	}
	pctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()
	if perr := sqlDB.PingContext(pctx); perr != nil {
		cerr := sqlDB.Close()
		if cerr != nil {
			return errors.Join(
				pkgerrors.Wrapf(perr, "gormx: mysql ping failed"),
				pkgerrors.Wrapf(cerr, "gormx: mysql close failed"),
			)
		}
		return pkgerrors.Wrapf(perr, "gormx: mysql ping failed")
	}
	return nil
}
