// register.go —— GORM 回调注册面（#225）。

package tenant

import (
	"errors"
	"fmt"
	"sync"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// registered 已注册实例登记（key = *gorm.DB 指针）。GORM 的回调 Register
// 对重名不报错（追加后重编译，重复谓词静默叠加）——双重注册 fail-loud
// 由本包自管：同一 *gorm.DB 注册两次返回 error。
var registered sync.Map

// ErrAlreadyRegistered 同一 *gorm.DB 重复注册租户回调时返回。
var ErrAlreadyRegistered = errors.New("gormx/tenant: 该 *gorm.DB 已注册租户回调")

// RegisterOption 注册选项（命名总则：XxxOption，防多包 co-import 冲突）。
type RegisterOption func(*registerConfig)

type registerConfig struct {
	column         string // 租户列名（默认 tenant_id）
	softDelete     bool   // 是否同时注入 deleted_at IS NULL 谓词（默认开）
	callbackPrefix string // 回调名前缀（默认 gormx:tenant）
}

// WithColumn 指定租户列名（默认 "tenant_id"）。非空约束：列名经 LookUpField
// 匹配模型字段，拼错表现为「谓词从不注入」——建议配合 Register 后的一条
// 冒烟断言（带租户列的表查询结果符合预期）。
func WithColumn(name string) RegisterOption {
	return func(c *registerConfig) {
		if name != "" {
			c.column = name
		}
	}
}

// WithSoftDeletePredicate 开关软删除谓词注入（默认开）。关掉用于宿主已有
// 自己的软删除方案（如 gormx.IntSoftDelete 字段类型法）不想叠加谓词的场景。
func WithSoftDeletePredicate(on bool) RegisterOption {
	return func(c *registerConfig) {
		c.softDelete = on
	}
}

// WithCallbackPrefix 指定 GORM 回调名前缀（默认 "gormx:tenant"）。同一进程
// 多库实例各注册一次时保持缺省即可（回调名挂在 *gorm.DB 实例上，不跨实例
// 冲突）；只有自定义回调编排与缺省名撞车时才需要改。
func WithCallbackPrefix(prefix string) RegisterOption {
	return func(c *registerConfig) {
		if prefix != "" {
			c.callbackPrefix = prefix
		}
	}
}

// Register 把租户强制注入回调注册到给定 *gorm.DB（每库实例一次）。注册
// 失败（重名等）返回 error——回调缺失 = 租户隔离静默失效，必须 fail-loud
// 拒绝半注册状态启动。
//
// 注入面：Query/Update/Delete（表模型带租户列 ⇒ 谓词无条件注入；显式
// WithoutScope(ctx) 跳过）；Create（tenant_id 零值从 ctx 回填，显式写入
// 优先）。软删除谓词（deleted_at IS NULL）随软删除开关一并注入。
func Register(db *gorm.DB, opts ...RegisterOption) error {
	if db == nil {
		return errors.New("gormx/tenant: Register 需要非 nil *gorm.DB")
	}
	if _, dup := registered.LoadOrStore(db, struct{}{}); dup {
		return ErrAlreadyRegistered
	}
	cfg := registerConfig{column: "tenant_id", softDelete: true, callbackPrefix: "gormx:tenant"}
	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
	}

	scope := func(tx *gorm.DB) {
		if ScopeSkipped(tx.Statement.Context) {
			return
		}
		if tx.Statement.Schema == nil {
			return // 裸 Table() 形态：无模型 schema 可判，跳过（此类站点须手工拼条件）
		}
		if tx.Statement.Schema.LookUpField(cfg.column) == nil {
			return // 全局基础设施表：无租户列，不受影响
		}
		tx.Statement.AddClause(clause.Where{Exprs: []clause.Expression{
			clause.Eq{Column: clause.Column{Name: cfg.column}, Value: FromContext(tx.Statement.Context)},
		}})
		if cfg.softDelete && tx.Statement.Schema.LookUpField("deleted_at") != nil && !DeletedIncluded(tx.Statement.Context) {
			tx.Statement.AddClause(clause.Where{Exprs: []clause.Expression{
				clause.Eq{Column: clause.Column{Name: "deleted_at"}, Value: nil},
			}})
		}
	}
	if err := db.Callback().Query().Before("gorm:query").Register(cfg.callbackPrefix+":scope", scope); err != nil {
		return fmt.Errorf("gormx/tenant: 注册 Query 回调失败: %w", err)
	}
	if err := db.Callback().Update().Before("gorm:update").Register(cfg.callbackPrefix+":scope", scope); err != nil {
		return fmt.Errorf("gormx/tenant: 注册 Update 回调失败: %w", err)
	}
	if err := db.Callback().Delete().Before("gorm:delete").Register(cfg.callbackPrefix+":scope", scope); err != nil {
		return fmt.Errorf("gormx/tenant: 注册 Delete 回调失败: %w", err)
	}

	setTenant := func(tx *gorm.DB) {
		if tx.Statement.Schema == nil {
			return
		}
		f := tx.Statement.Schema.LookUpField(cfg.column)
		if f == nil {
			return
		}
		if v, zero := f.ValueOf(tx.Statement.Context, tx.Statement.ReflectValue); !zero {
			if s, ok := v.(string); ok && s != "" {
				return // 显式写入租户优先（跨租户搬运等高级场景）
			}
		}
		tx.Statement.SetColumn(cfg.column, FromContext(tx.Statement.Context))
	}
	if err := db.Callback().Create().Before("gorm:create").Register(cfg.callbackPrefix+":set", setTenant); err != nil {
		return fmt.Errorf("gormx/tenant: 注册 Create 回调失败: %w", err)
	}
	return nil
}
