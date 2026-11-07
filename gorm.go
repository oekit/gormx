package gormx

import (
	"fmt"
	"time"

	"github.com/oekit/logx"
	"gorm.io/gorm"
	gormLogger "gorm.io/gorm/logger"
)

var gormLog = logx.Delegating("gormx")

// OpenOption 配置 Open 的行为（函数选项模式）。
type OpenOption func(*openConfig)

type openConfig struct {
	gormOptions     []gorm.Option
	maxIdleConns    int
	maxOpenConns    int
	connLifetime    time.Duration
	connIdleTime    time.Duration
	configurePool   bool
	maxOpenConnsSet bool // WithConnectionPool 显式设置过 MaxOpenConns（#97）
	logger          gormLogger.Interface
}

// WithLogger 替换 Open 注入的默认 Logger（如带列级脱敏的自定义实例，
// #106：gormx.NewDefaultLogger(gormx.WithRedactColumns("password", ...))）。
// 传入的 Logger **原样使用**（不再强制 Info 级——级别与脱敏配置尊重调用方）。
func WithLogger(l gormLogger.Interface) OpenOption {
	return func(c *openConfig) {
		c.logger = l
	}
}

// WithGormOptions 追加底层 gorm.Option。
func WithGormOptions(opts ...gorm.Option) OpenOption {
	return func(c *openConfig) {
		c.gormOptions = append(c.gormOptions, opts...)
	}
}

// WithConnectionPool 配置连接池参数。
//
// ⚠️ 参数顺序与 database/sql 习惯对齐（#76）：**maxOpen 在前**，同
// SetMaxOpenConns / SetMaxIdleConns 的声明次序（db 主资源在先、池缓存次之）。
// v0.8.0 及以前是 idle 在前——传反不报错、只会静默把 MaxOpenConns 设成 idle 值
// （idle > open 的组合 database/sql 不报错，行为是 idle 永远达不成）。
func WithConnectionPool(maxOpenConns, maxIdleConns int, connMaxLifetime time.Duration) OpenOption {
	return func(c *openConfig) {
		c.maxOpenConns = maxOpenConns
		c.maxIdleConns = maxIdleConns
		c.connLifetime = connMaxLifetime
		c.configurePool = true
		c.maxOpenConnsSet = true
	}
}

// WithConnMaxIdleTime 配置连接最大空闲时间。
// 空闲超过此时间的连接会被关闭，防止 stale 连接积累。
func WithConnMaxIdleTime(d time.Duration) OpenOption {
	return func(c *openConfig) {
		c.connIdleTime = d
		c.configurePool = true
	}
}

// Open 使用指定的 dialector 打开数据库连接，自动注入默认日志。
//
// 默认行为：
//   - 注入默认 Logger（Info 级别）
//   - 零 tracing 注入：需要 SQL Span 经 github.com/oekit/tracing/gormotel
//     显式挂载
//
// ⚠️ **Info 级 SQL 日志含完整绑定值**（#106）：INSERT/UPDATE 的 VALUES
// 全量进日志（password/token/request_body 等敏感列随写路径外泄，日志采集
// 外送进一步放大泄漏面）。敏感列经 WithLogger + WithRedactColumns 脱敏：
//
//	db, err := gormx.Open(mysql.Open(dsn), gormx.WithLogger(
//	    gormx.NewDefaultLogger(gormx.WithRedactColumns("password", "token"))))
//
// ⚠️ 连接池配置是 **opt-in**（#75）：不传 WithConnectionPool ⇒ MaxOpenConns=0
// （database/sql 缺省，**无上限**）——多副本部署下必然打满 PG max_connections
// （too many clients）。Open 对 MaxOpenConns=0 打 WARN 且**区分两形态**（#97）：
// 未传 option（去调用代码补显式配置）vs 显式传 0（去查上游配置项来源）；
// 生产部署必须显式配置：
//
//	db, err := gormx.Open(
//	    mysql.Open(dsn),
//	    gormx.WithConnectionPool(100, 10, time.Hour), // maxOpen 在前（#76）
//	)
//
// 需要 OTel 追踪时（装饰器显式接入，无 SDK 时 noop）：
//
//	db, err := gormx.Open(mysql.Open(dsn))
//	if err := db.Use(gormotel.NewPlugin()); err != nil { ... }
func Open(dialector gorm.Dialector, opts ...OpenOption) (*gorm.DB, error) {
	cfg := &openConfig{}
	for _, opt := range opts {
		opt(cfg)
	}

	gormCfg := gorm.Config{}
	if cfg.logger != nil {
		// 显式传入的 Logger 原样使用（级别/脱敏配置尊重调用方，不强制 Info）
		gormCfg.Logger = cfg.logger
	} else {
		gormCfg.Logger = NewDefaultLogger().LogMode(gormLogger.Info)
	}
	for _, gormOpt := range cfg.gormOptions {
		if err := gormOpt.Apply(&gormCfg); err != nil {
			return nil, fmt.Errorf("apply gorm option: %w", err)
		}
	}

	db, err := gorm.Open(dialector, &gormCfg)
	if err != nil {
		gormLog.Error("failed opening connection to db", "err", err)
		return nil, err
	}

	if cfg.configurePool {
		sqlDB, err := db.DB()
		if err != nil {
			return nil, err
		}
		sqlDB.SetMaxIdleConns(cfg.maxIdleConns)
		sqlDB.SetMaxOpenConns(cfg.maxOpenConns)
		sqlDB.SetConnMaxLifetime(cfg.connLifetime)
		if cfg.connIdleTime > 0 {
			sqlDB.SetConnMaxIdleTime(cfg.connIdleTime)
		}
	}

	// 连接池静默退化守卫（#75/#97）：池配置是 opt-in，不传 WithConnectionPool
	// （或只传 WithConnMaxIdleTime）⇒ MaxOpenConns=0 无上限——「配置能力藏在
	// 库深处 =没人知道它是什么、也就没人能发现它没生效」，出事形态是生产事故
	// （多副本打满 PG max_connections）而非编译错误。WARN 不拦行为（单实例
	// 开发/批处理场景可能确有意为之），把「没生效」变成可见事实。
	//
	// 两形态分案（#97）：maxOpenConns==0 可能是「没传 option」，也可能是
	// 「显式传了 0」（如部署环境没设 DB_MAX_OPEN_CONNS 等 env 时配置值为 0
	// 传到调用点）——两个形态的排查动作完全相反：前者去调用代码补显式配置，
	// 后者调用代码已经写好，要查的是**上游配置项来源**（env/文件是否漏配）。
	// 共用一条文案会把排查者指引到已完好的调用点，方向被带偏。
	switch {
	case cfg.maxOpenConns > 0:
		// 已配置上限，无事
	case cfg.maxOpenConnsSet:
		gormLog.Warn("连接池显式配置为 MaxOpenConns=0（database/sql 语义 = 无上限），多副本部署下会打满 PG max_connections（too many clients）——调用点已显式配置，请检查配置项来源（env/文件是否漏配），而非改调用代码")
	default:
		gormLog.Warn("连接池未设置 MaxOpenConns 上限：database/sql 缺省无上限，多副本部署下会打满 PG max_connections（too many clients）——生产部署建议显式 gormx.WithConnectionPool(maxOpen, idle, lifetime)")
	}

	return db, nil
}
