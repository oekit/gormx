package gormx

import (
	"time"

	"github.com/oekit/logx"
	bizerrors "github.com/oekit/pkg/errors"
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
	// #229：逐项 set 标记——此前只传 WithConnMaxIdleTime 也把 configurePool
	// 置真，池配置块用「未设置过的 0」调 SetMaxIdleConns(0)（database/sql
	// 语义 = 不保留任何空闲连接），想清的 stale 连接根本不存在。
	maxIdleConnsSet bool
	connLifetimeSet bool
	connIdleTimeSet bool
	logger          gormLogger.Interface
	// skipProbe 跳过构造期连通性验证（WithSkipProbe，#262，当前仅
	// OpenMySQL 消费）。
	skipProbe bool
}

// WithLogger 替换 Open 注入的默认 Logger（如带列级脱敏的自定义实例，
// #106：gormx.NewDefaultLogger(gormx.WithRedactColumns("password", ...))）。
// 传入的 Logger **原样使用**（不再强制 Info 级——级别与脱敏配置尊重调用方）。
func WithLogger(l gormLogger.Interface) OpenOption {
	return func(c *openConfig) {
		c.logger = l
	}
}

// WithSkipProbe 跳过构造期连通性验证（#262，当前仅 OpenMySQL 消费——
// 构造后的显式 Ping）。仅供测试 / 离线场景使用，生产路径不要关闭：
// 构造期 Ping 是「配置错连 / 服务不可达却带着死实例继续跑」的最后防线。
// 注意跳过的只是 OpenMySQL 追加的验证，gorm.Open 自身的拨号行为不变。
func WithSkipProbe() OpenOption {
	return func(c *openConfig) {
		c.skipProbe = true
	}
}

// WithGormOptions 追加底层 gorm.Option（如
// &gorm.Config{SkipDefaultTransaction: true}）。
//
// ⚠️ gorm.Config 的 Apply 是**整结构体覆盖**（*config = *c）：多个
// *gorm.Config 同时传入时后者会整体覆盖前者，请合并成一个传入。
// gormx 在应用完所有 gorm.Option 之后才补默认值（#268）：WithLogger 传入的
// 脱敏 Logger 不会被 WithGormOptions 清掉；未显式提供 Logger 时保留
// gorm.Option 自带的 Logger，两者都缺省才使用 gormx 默认 Logger。
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
		c.maxIdleConnsSet = true
		c.connLifetimeSet = true
	}
}

// WithConnMaxIdleTime 配置连接最大空闲时间。
// 空闲超过此时间的连接会被关闭，防止 stale 连接积累。
func WithConnMaxIdleTime(d time.Duration) OpenOption {
	return func(c *openConfig) {
		c.connIdleTime = d
		c.connIdleTimeSet = true
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

	// #268：先应用底层 gorm.Option，再仅对仍为零值的字段补 gormx 默认。
	// gorm.Config 的 Apply 是整结构体覆盖（*config = *c）——此前先设 gormx
	// 默认 Logger 再 Apply，WithGormOptions(&gorm.Config{...}) 会把脱敏 Logger
	// 整块清成 nil（gorm.Open 兜底为 logger.Default，password/token 随 SQL
	// 日志外泄）。WithGormOptions 的语义是「追加底层选项」，不得反向清掉
	// gormx 的默认值。
	gormCfg := gorm.Config{}
	for _, gormOpt := range cfg.gormOptions {
		if err := gormOpt.Apply(&gormCfg); err != nil {
			return nil, bizerrors.Wrapf(err, "apply gorm option")
		}
	}
	switch {
	case cfg.logger != nil:
		// WithLogger 显式传入的 Logger 原样使用（级别/脱敏配置尊重调用方，
		// 不强制 Info），并优先于 gorm.Option 携带的 Logger——它是 gormx 层
		// 最具体的日志诉求（#106）。
		gormCfg.Logger = cfg.logger
	case gormCfg.Logger == nil:
		// 两个来源都没提供 Logger 时才补 gormx 默认（Info 级）。
		gormCfg.Logger = NewDefaultLogger().LogMode(gormLogger.Info)
	}

	db, err := gorm.Open(dialector, &gormCfg)
	if err != nil {
		gormLog.Error("failed opening connection to db", logx.ErrKey, err)
		return nil, err
	}

	if cfg.configurePool {
		sqlDB, err := db.DB()
		if err != nil {
			return nil, err
		}
		// #229：逐项按 set 标记——只传单项 option（如 WithConnMaxIdleTime）
		// 时，未设置的项不再用「零值」下发。database/sql 缺省 MaxIdleConns=2
		// 被 SetMaxIdleConns(0) 覆盖成「不保留任何空闲连接」是本缺口的事故面。
		if cfg.maxOpenConnsSet {
			sqlDB.SetMaxOpenConns(cfg.maxOpenConns)
		}
		if cfg.maxIdleConnsSet {
			sqlDB.SetMaxIdleConns(cfg.maxIdleConns)
		}
		if cfg.connLifetimeSet {
			sqlDB.SetConnMaxLifetime(cfg.connLifetime)
		}
		if cfg.connIdleTimeSet && cfg.connIdleTime > 0 {
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
