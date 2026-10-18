package gormrepo

import (
	"context"
	"fmt"

	"gorm.io/gorm"
)

// RepositoryOption 配置 GormRepository（命名对齐仓内硬规则，避免裸 Option）。
type RepositoryOption[T any] func(*gormRepoConfig[T])

// gormRepoConfig 仓储构造配置。
type gormRepoConfig[T any] struct {
	// queryDecorator 从 ctx 取当次请求身份，返回 gorm 行级 scope。
	// 返回 nil 表示本次不加 scope（如 admin 直通）。
	queryDecorator func(ctx context.Context) func(*gorm.DB) *gorm.DB

	// scopeRequired fail-closed：未配置 decorator、或 decorator 返回 nil
	// （裸查询）时显式报错，而不是放行。
	scopeRequired bool
}

// WithQueryDecorator 挂接查询装饰器（行级 scope 自动注入）。
//
// fn 从 ctx 取当次请求身份（uid/team/tenant），返回 gorm scope；
// **返回 nil 表示本次不加 scope**（如 admin 直通）。
//
// 应用面覆盖 GetByID/GetOne/GetByCondition/List/Exists/Count/Update/
// UpdateFields/Delete/BatchDelete/GetByIDForUpdate——**含按 ID 的
// Get/Update/Delete**（越权最常出在「按主键查时忘了带 scope」）。
// Create/BatchCreate 不套 scope（插入路径的身份注入属于 model hook）。
//
// 与 TxRepository 兼容：RunInTx 产出的事务仓储、WithTx 副本均继承同一
// decorator，scope 在 tx 之上应用。
//
// 默认（未配置 decorator）为 passthrough：单租户服务零负担。
// 多租户请配 WithQueryDecorator；需要 fail-closed 时再加 WithScopeRequired。
func WithQueryDecorator[T any](fn func(ctx context.Context) func(*gorm.DB) *gorm.DB) RepositoryOption[T] {
	return func(c *gormRepoConfig[T]) { c.queryDecorator = fn }
}

// WithScopeRequired 开启 fail-closed：以下情况显式报错而不是放行——
//
//   - 未配置 WithQueryDecorator（构造即缺装饰器）；
//   - decorator 返回 nil（本次查询成了裸查询）。
//
// admin 直通请让 decorator 返回空 scope（`func(db *gorm.DB) *gorm.DB {
// return db }`，非 nil）而非 nil，或不要开启本选项。
func WithScopeRequired[T any]() RepositoryOption[T] {
	return func(c *gormRepoConfig[T]) { c.scopeRequired = true }
}

// scopedDB 应用查询装饰器，返回带 scope 的 gorm.DB。
//
// fail-closed 规则见 WithScopeRequired；默认无 decorator 时 passthrough。
func (r *GormRepository[T, ID]) scopedDB(ctx context.Context) (*gorm.DB, error) {
	db := r.db.WithContext(ctx)
	if r.cfg.queryDecorator == nil {
		if r.cfg.scopeRequired {
			return nil, fmt.Errorf("gormrepo: WithScopeRequired 要求查询装饰器，但未配置 WithQueryDecorator")
		}
		return db, nil
	}
	scope := r.cfg.queryDecorator(ctx)
	if scope == nil {
		if r.cfg.scopeRequired {
			return nil, fmt.Errorf("gormrepo: WithScopeRequired 拒绝裸查询（decorator 返回 nil）")
		}
		return db, nil
	}
	return db.Scopes(scope), nil
}
