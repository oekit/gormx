// Package tenant —— 多租户强制谓词注入（#225，与 scoper 互补）。
//
// scoper（#102）是**调用侧逐查询**显式 Scopes(rule.Scope(...)) 的行级可见
// 面；本包是**存储层强制注入**面：经 Register 挂上 GORM 回调后，谓词
// tenant_id = tenant.FromContext(ctx) 进每个 Query/Update/Delete——新查询
// 漏挂从「安全事故」变「不可能」。安全级谓词不能靠每个调用点记得挂。
//
// 机制要点（提出方 argus v8.0.0 生产实证，50+ 仓轮询/多租户任务流水在役）：
//
//   - 表模型带 tenant_id 列 ⇒ 谓词无条件注入；无该列（全局基础设施表）
//     不受影响——零声明接入，存量全局表不用改；
//   - 无租户声明（ctx 未写入）⇒ Default 桶：fail-closed 方向是「无声明 =
//     默认租户」而非「无声明 = 全可见」；
//   - 显式逃生门 WithoutScope（跨租户对账/清理，调用方注释写明理由）；
//   - 软删除谓词同事注入：表带 deleted_at 列且 ctx 未 WithDeleted ⇒
//     deleted_at IS NULL；逃生门 WithDeleted 供清理/恢复/取证面；
//   - Create 回填：tenant_id 零值时从 ctx 归一值回填，显式写入优先；
//   - 注册 fail-loud：回调重名/失败即返回 error——回调缺失 = 租户隔离
//     静默失效，必须拒绝半注册状态启动；
//   - Raw/Exec 裸 SQL 不经过回调：裸 SQL 站点的租户条件须手工拼（与
//     scoper 相同的治理口径声明）。
package tenant

import (
	"context"
	"strings"
)

// Default 缺省租户：未声明租户的一切主体（存量数据行、无租户声明的调用、
// 内部执行路径）都归入本租户。ID 显式字符串而非空串——空串在 SQL 谓词与
// 缓存键拼接里语义含混（NULL/”/missing 三态），显式 "default" 单态可判。
const Default = "default"

// ctxKey 租户 ctx 键（私有类型防跨包键碰撞）。
type ctxKey struct{}

// skipKey 强制跳过租户谓词注入的 ctx 键：控制面跨租户运维操作（全库对账/
// 列举）的显式逃生门。机制而非纪律——普通查询永远不带这个键。
type skipKey struct{}

// includeDeletedKey 软删除可见性 ctx 键（默认查询不可见 deleted_at 非空行）。
type includeDeletedKey struct{}

// Normalize 租户 ID 归一：空/纯空白 → Default；其余去空白原样返回。
// 唯一归一出口——存储谓词、缓存键前缀、审计字段全走它，杜绝三态漂移。
func Normalize(id string) string {
	if t := strings.TrimSpace(id); t != "" {
		return t
	}
	return Default
}

// NewContext 把租户写入 ctx（写入前归一）。
func NewContext(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, ctxKey{}, Normalize(id))
}

// FromContext 从 ctx 取租户 ID；未写入（内部路径/旧调用）→ Default。
func FromContext(ctx context.Context) string {
	if ctx == nil {
		return Default
	}
	if v, ok := ctx.Value(ctxKey{}).(string); ok && v != "" {
		return v
	}
	return Default
}

// WithoutScope 标记 ctx 为「跳过租户谓词注入」（逃生门；调用方须在注释里
// 写明跨租户理由并接受审计面）。Register 挂的回调认这个标记。
func WithoutScope(ctx context.Context) context.Context {
	return context.WithValue(ctx, skipKey{}, true)
}

// ScopeSkipped 判定 ctx 是否显式跳过租户谓词。
func ScopeSkipped(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	v, _ := ctx.Value(skipKey{}).(bool)
	return v
}

// WithDeleted 标记 ctx「查询包含软删除行」（逃生门；调用方须在注释写明
// 理由）。软删除谓词注入认这个标记。
func WithDeleted(ctx context.Context) context.Context {
	return context.WithValue(ctx, includeDeletedKey{}, true)
}

// DeletedIncluded 判定 ctx 是否显式要求包含软删除行。
func DeletedIncluded(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	v, _ := ctx.Value(includeDeletedKey{}).(bool)
	return v
}

// Key 以租户为前缀拼复合键（缓存键/文件路径段/副作用资源串共用形态）：
// 返回 "<id>/<rest>" 统一形态，同坐标资源跨租户天然分桶。rest 为空返回 "<id>"。
func Key(id, rest string) string {
	id = Normalize(id)
	if rest == "" {
		return id
	}
	return id + "/" + rest
}
