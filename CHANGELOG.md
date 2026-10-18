# Changelog — oekit/gormx

所有 notable 变更记录如下。格式参考 [Keep a Changelog](https://keepachangelog.com/)。

## [v0.5.5] — 2026-10-17

- fix(deps): logx 地板 v0.5.0 → v0.6.0（零代码改动）

## [v0.5.4] — 2026-10-17

- pkg 地板 v0.10.0 → v0.11.0（零代码改动）

## [v0.5.3] — 2026-10-17

- fix(deps): pkg 地板 v0.9.3 → v0.10.0（拆分版，零代码改动）

## [v0.5.2] — 2026-10-17

- fix(deps): 内部依赖地板全量对齐最新——pkg v0.1.3 → v0.9.3、logx v0.1.9 → v0.5.0；go 最低版本降至 1.24.0（gorm 生态无回抬，自然落点）

## [v0.5.1] — 2026-10-17

仓库结构重置后的首个版本（历史与旧版本号 v0.1.0–v0.3.0 已清空重做，从 v0.5.1 起算）。

### 目录结构

```
github.com/oekit/gormx                 # package gormx — 连接池增强/分页/事务（模块根）
├── crud/                              # package crud — 框架无关 CRUD Service
└── gormrepo/                          # package gormrepo — GORM 仓储实现
```

- **删除重复层** `crud/gormrepo/`（与根 `gormrepo/` 字节级重复的副本）
- 仓储实现的规范 import 路径统一为 `github.com/oekit/gormx/gormrepo`

### 能力面

- `gormx.Open` / `OpenMySQL` — 函数选项连接池（`WithConnectionPool` 等）
- `gormx.Paginate` — 单查询分页
- `gormx.InTransactionContext` — 事务上下文
- `gormx.FilterBuilder` — 过滤条件构建
- `gormx.Model` — 通用模型基座
- `crud.NewBaseService[T, CreateReq, UpdateReq]` — 框架无关 CRUD Service
- `crud.Repository` / `TxRepository` — 仓储接口；TxRepository 解锁
  Update 的「事务 + FOR UPDATE 行锁」防丢更新，afterUpdate 钩子失败整体回滚
- `gormrepo.NewGormRepository[T](db, opts...)` — GORM 仓储实现
- `gormrepo.WithQueryDecorator` / `WithScopeRequired` — 多租户行级隔离：
  scope 自动注入（含按 ID 的 Get/Update/Delete），fail-closed 可选
- 零 tracing 注入：SQL Span 经 `github.com/oekit/tracing/gormotel` 显式挂载

### 迁移（从旧路径 / 重复层）

| 旧 | 新 |
|---|---|
| `github.com/oekit/gormx/crud/gormrepo` | `github.com/oekit/gormx/gormrepo` |
| `github.com/oekit/gorm`（已更名历史） | `github.com/oekit/gormx` |
