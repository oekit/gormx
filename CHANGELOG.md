# Changelog — oekit/gormx

所有 notable 变更记录如下。格式参考 [Keep a Changelog](https://keepachangelog.com/)。

## [v0.8.0] — 2026-11-04

- fix(sqlite)!: 纯 Go sqlite 入口移出根包，新增 `gormx/sqlitedrv` 子包（#72）——
  根包 `gormx`、`crud/`、`gormrepo/` 生产代码**零 sqlite 驱动链接**
- ⚠️ breaking：`gormx.OpenPureGoSQLite`（根包入口）移除。迁移：改 import
  `github.com/oekit/gormx/sqlitedrv`，调用 `sqlitedrv.Open(dsn, opts...)`
  （迁移别名 `sqlitedrv.OpenPureGoSQLite` 同签名，可只改 import 路径）

**根因**：glebarez 系（`github.com/glebarez/go-sqlite`）与 `modernc.org/sqlite`
都在 `init()` 里 `sql.Register("sqlite", ...)` ⇒ 同一二进制链接两者启动即
`panic: sql: Register called twice for driver "sqlite"`。旧入口住根包 ⇒
只要用 gormx（哪怕只用 MySQL/CRUD/事务）就带上 glebarez 的注册副作用，
与任何直接使用 `modernc.org/sqlite` 的库（agent-memory 等）组合启动即炸
（消费方实证：code-review-service）。

**判据**（可执行契约，非文档承诺）：

- `deps_sqlite_link_test.go`：AST 静态断言核心包生产代码零 sqlite 驱动
  import（含正对照——sqlitedrv 确实接线 glebarez，防门禁空转）；
- `sqlite_conflict_repro_test.go`：炸点复现对——正对照「根包 + modernc
  干净启动」（修复前真红）、负对照「sqlitedrv + modernc 仍 panic」（边界
  如实固化：驱动注册副作用是上游行为，显式 import sqlitedrv 的二进制
  不得再直接 import modernc）；
- 两项判据均经变异自证：根包混入 glebarez ⇒ 门禁与复现正对照精确转红。

**边界**：`sqlitedrv` 与 `modernc.org/sqlite` 仍不可同二进制共存（详见
包文档）；mattn 系（`gorm.io/driver/sqlite`）注册 "sqlite3" 不受影响。

关联问题单：gormx#72。

## [v0.7.1] — 2026-11-03

- 修 FilterBuilder.Add OR 片段自动加括号，消灭 `(... AND a) OR b` 击穿组合条件（#63）
- 新增 WhereGroup/OrGroup 分组条件、FIND_IN_SET 白名单与 WhereRaw 逃生舱（#64）

关联问题单：gormx#63、gormx#64。

## [v0.7.0] — 2026-11-03

- feat(crud)!: Writer 写方法回传 RowsAffected + UpdateOption 列白名单（immutable 列保护）+ ErrMapper 错误出口可映射消费方码表（#60 #61 #62）
- ⚠️ breaking：crud.Writer 四个写方法签名加 (int64, error)；BaseService.Delete 缺失记录由静默成功改 404

关联问题单：gormx#60、gormx#61、gormx#62。

## [v0.6.6] — 2026-11-02

- build: 矩阵内依赖地板 `pkg` v0.31.0 → v0.31.1（维护轮全量对齐；本版不含 API 变更）

## [v0.6.5] — 2026-11-01

- feat(paginate): Paginate 自动 Model 推导（忘设 Model 不再报 Table not set）+ MapQueryError 统一 404/500 映射；queryFn 容忍 nil

## [v0.6.4] — 2026-10-28

- fix(deps): 主模块去掉 CGO 驱动 `gorm.io/driver/sqlite`，测试改用纯 Go 驱动
  `github.com/glebarez/sqlite` —— 该驱动此前**生产代码零使用**（只在测试里），
  却是主模块 go.mod 的直接依赖 ⇒ 后果：① 本模块自己的测试套件**跑不了免 CGO
  环境**（实测 `CGO_ENABLED=0 go test ./...` 全部失败："go-sqlite3 requires cgo to
  work. This is a stub"），而 NAS 套件 / 容器静态构建（`CGO_ENABLED=0`）正是消费方
  ⇒ **消费方能构建的环境，库自己却不可测**；② 判据 `IsUniqueViolation` 的 sqlite
  分支声称覆盖「glebarez 纯 Go 驱动」的文案，而测试只跑 CGO 驱动 ⇒ **验证面 ≠
  承诺面**（纯 Go 驱动的文案若变化，库内测不出来）。改用纯 Go 驱动后：测试套件可在
  `CGO_ENABLED=0` 下运行，sqlite 文案由**承诺的那个驱动**直接验证
  （实际文案 "constraint failed: UNIQUE constraint failed: <表>.<列> (2067)"），
  主模块不再依赖任何 CGO 驱动（见 oekit/gormx#36）
- feat(sqlite): 新增 `OpenPureGoSQLite(dsn, opts...)` —— 免 CGO 的 sqlite 打开入口
  （与 `OpenMySQL` 对称），消费方不必各自拼驱动、拼出与判据口径错配的组合

## [v0.6.3] — 2026-10-28

- fix(gormrepo): 计数语句不再被 `Selects` 污染——GORM 的 `Count` 在 `Statement.Selects` 非空时生成 `COUNT(<列>)` 而非 `count(*)` ⇒ 该列为 NULL 的行被漏计（实测 3 行含 1 行 `name IS NULL` ⇒ total 报 2），列名写错时直接 SQL 报错 `no such column` 令计数整体不可用；分组计数同样受害。修法：计数路径一律丢弃 `Selects`（计数语义是「行数」，与查哪些列无关），列表路径不变

## [v0.6.2] — 2026-10-27

- fix: `IsUniqueViolation` 补 sqlite 分支——glebarez/sqlite（纯 Go 驱动）撞唯一键的文案是
  `UNIQUE constraint failed: <表>.<列>`，既不映射 `gorm.ErrDuplicatedKey`（未开
  TranslateError），也不含 mysql/pg 的措辞 ⇒ 原实现**判不出来**。消费方实证：
  pigeonbox/core 默认库就是 sqlite，其手搓判据正是为补这一段（oekit/gormx#30）。
  同时补负例用例，确保 `CHECK constraint failed` / `FOREIGN KEY constraint failed`
  这类普通约束错误不被宽判据误判成唯一冲突。

## [v0.6.1] — 2026-10-19

- build: 矩阵内依赖地板对齐（align_floors 不动点）：logx v0.6.0 → v0.7.0、pkg v0.11.0 → v0.15.1

## [v0.6.0] — 2026-10-19

**BREAKING**: crud/gormrepo 主键类型泛型化（#7 问题 1）——接口全链路加 `ID comparable`
泛型参数，文本主键（TEXT PRIMARY KEY）/UUID 等项目可直接实现 crud 契约，不再被写死 int64。

- crud：`Reader[T]`/`Writer[T]`/`Repository[T]`/`TxRepository[T]`/`Service[T, C, U]` →
  `[T, ID comparable]`；`BaseService`/`NewBaseService`/Set*Hook 签名同步（`id int64` → `id ID`）。
  存量调用点机械迁移：`NewBaseService[T, C, U](repo)` → `NewBaseService[T, int64, C, U](repo)`
- gormrepo：`GormRepository[T]` → `[T, ID comparable]`；`WithQueryDecorator`/`WithScopeRequired`
  维持 `[T]`（decorator 不接触 ID，无需迁移）
- **行为加固（同波）**：GetByID/Delete/BatchDelete 主键条件从 GORM 内联值改为
  `clause.PrimaryColumn`（schema 解析 + 参数化）——GORM 将内联非数字 string 按 SQL 片段解析，
  内联形态对文本主键是注入面/语法错误；int64 路径 SQL 语义不变；GetByID 未命中错误格式
  `id=%d` → `id=%v`
- 根包 `model.go`（BaseModel 系列）与 `singleton.go`（Identifier）**保留 int64**：
  自增主键便捷基类，文本主键项目自定义实体 struct 即可
- 新增文本主键回归测试（string PK 全路径：GetByID/UpdateFields/Delete/BatchDelete/
  BaseService 事务路径/-race）
- 消费方波次：data v0.14.0（crud 壳别名参数化 + cached ID 泛型化，go 地板 1.25+）、
  hertz v0.7.0（crudh WithIDParser）

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
- `gormrepo.WithQueryDecorator` / `WithScopeRequired` — 多租户行级隔离（#4）：
  scope 自动注入（含按 ID 的 Get/Update/Delete），fail-closed 可选
- 零 tracing 注入：SQL Span 经 `github.com/oekit/tracing/gormotel` 显式挂载

### 迁移（从旧路径 / 重复层）

| 旧 | 新 |
|---|---|
| `github.com/oekit/gormx/crud/gormrepo` | `github.com/oekit/gormx/gormrepo` |
| `github.com/oekit/gorm`（已更名历史） | `github.com/oekit/gormx` |
