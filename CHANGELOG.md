

## [Unreleased]

- fix(crud): Order/GroupBy/Select/Preload 标识符白名单 fail-closed（#154）——白名单外的排序/分组/投影/关联名整条丢弃并按 #135 口径经 Err() 累计、Strict() 当场 panic，前端可控字段名不再原样内插 SQL；合法调用（字段 [ASC|DESC]、点分关联路径、裸标识符 / * / t.* 投影）原样透传向后兼容

关联问题单：oekit/gormx#154。

- fix(crud): Page 入口归一，响应信封与实际 LIMIT/OFFSET 口径一致（#155）——Page(0, 0)（或负值）此前 SQL 打 LIMIT 20、信封却按「未分页」0/0 上报，调用方拿 20 条以为拿到全量；页长超 500 时信封报原值而 SQL 已被钳到 500，同样是谎报。入口 Normalize 后 GetPage/Spec.Page 回写生效值，未调用 Page() 的「未分页」0/0 语义不变

关联问题单：oekit/gormx#155。

## [v0.11.11] — 2026-11-10

- - build: 依赖地板对齐（floor sync，无代码变更）

## [v0.11.10] — 2026-11-10

- - fix(redact): skipQuoted 统一引号区段跳过——含引号/逃逸的 INSERT 元组不再静默 no-op 泄漏原文；不可解析整段 *** 失败闭合（#138）

关联问题单：gormx#138。

## [v0.11.9] — 2026-11-10

- - build: 依赖地板对齐（floor sync，无代码变更）

## [v0.11.8] — 2026-11-10

- - fix(pagination): Paginate 计数语句丢弃 Selects——total 与实际返回条数一致，页数不再整体偏一（#136）
- - feat(crud): Query.Err() 累计 fail-closed 渲染错误 + Query.Strict() 渲染 1=0 当场 panic——恒假条件对调用方可见（#135）

关联问题单：gormx#135、gormx#136。

## [v0.11.7] — 2026-11-08

- fix(subquery): SubqueryIn/DeleteBySubquery 的 column 参数过标识符白名单 fail-closed（#122，与 crud Where/Or 的 #100 同款纪律）——mysql 派生表路径经 Select(string) 原样渲染，白名单外的值（逗号多列/括号表达式/子查询）此前会被原样拼进 `SELECT <column> FROM (...) AS gormx_dt` 的裸拼面；白名单 `^[A-Za-z_][A-Za-z0-9_.]*$`（含表限定名），拒绝时经 AddError 报错且 SQL 零泄漏
- test(migrate): 测试库 DSN 加 uuid 轮次唯一后缀（#117，同根包 newTestDB 先例）——此前 DSN 只按 t.Name() 命名 + cache=shared：共享缓存内存库进程存活期内同 DSN 指向同一个库、t.Name() 在 -count=N 下每轮相同 ⇒ 第二轮撞第一轮残留数据（UNIQUE constraint failed，3 用例恒红）；本版起 migrate 包 -count=2 全绿

关联问题单：gormx#117、gormx#122。

## [v0.11.6] — 2026-11-08

- build: 依赖地板对齐——本版把 oekit 依赖地板抬到各依赖当前最新已发布版本（matrix-tools/scripts/align_floors.py 维护轮）

## [v0.11.5] — 2026-11-08

- feat: 模糊查询参数原语 EscapeLike/LikeContains/LikePrefix/LikeSuffix（#114）——LIKE 通配符转义与四种匹配形态，消费方不再手写转义

关联问题单：gormx#114。

## [v0.11.4] — 2026-11-08

- docs(singleton): #112 收口——该单为 #99 的重报（Save/GetOrCreate 收敛只认 gorm.ErrDuplicatedKey，sqlite/未开 TranslateError 下唯一冲突是裸文案、收敛从不触发），修复已随 v0.11.3（PR #107）落地（判据改 IsUniqueViolation——其内含 errors.Is(gorm.ErrDuplicatedKey) 分支，语义覆盖 #112 建议的 \|\| 组合式；含 sqlite 裸文案冲突的确定性收敛回归测试）；本版把 singleton.go 与回归测试的注释标注对齐重报单号，无行为变更

关联问题单：gormx#99、gormx#112。

## [v0.11.3] — 2026-11-07

- feat(datatypes): 新子包——JSONSlice[T]/JSONMap[K,V] 泛型 JSON 列类型，Scan/Value 五件套收编，nil↔NULL 与空集合可区分（#80）
- feat(subquery): SubqueryIn/DeleteBySubquery 方言安全子查询 DML 构造器——MySQL 1093 自动派生表包装，含批量 CAS 认领 recipe（#81）
- fix(pagination): Paginate 无 ORDER BY 时按主键注入 tiebreaker——OFFSET/LIMIT 无序未定义关闭（#95）
- fix(migrate): IsIgnorableError 类型化判定——MySQL 错误号/PG SQLSTATE 优先，纯文案匹配降为兜底（#96）
- fix(gorm): 连接池 WARN 区分「未传 option」与「显式传 0」——显式配置零值不再被误导成漏配（#97）
- feat(migrate): Options.LockKey——非空时全量执行包进 RunWithAdvisoryLock，多副本 DDL 串行化一等公民（#98）
- fix(singleton): SingletonRepo 收敛判据改 IsUniqueViolation——sqlite/未开 TranslateError 下收敛不再失效（#99）
- fix(crud): Where/Or 字段名标识符白名单校验——白名单外 fail-closed（#100）
- fix(pagination): Paginate 不再原地 Normalize 调用方共享 PageRequest——只读副本计算（#101）
- feat(scoper): 新子包——Rule[T] 单源驱动 SQL WHERE 面与内存 Allows 面，行级 scope 双轨关闭（#102）
- feat(pagination): gormx.Page 参数类型——NormalizePage+Offset/Limit/Scope/Request 一体（#103）
- docs(migrate): SplitStatements DELIMITER 局限声明 + 命中 DELIMITER 打根因 WARN（#104）
- feat(patch): SavePatch[M] 指针字段补丁部分更新——nil 不触碰、显式零值可写，Save 清零/手搓 updates map 两难消灭（#105）
- feat(logger): WithRedactColumns 列级脱敏 + Open godoc「Info=含值」警示——敏感列不再随 SQL 日志外泄（#106）

关联问题单：gormx#80、gormx#81、gormx#95、gormx#96、gormx#97、gormx#98、gormx#99、gormx#100、gormx#101、gormx#102、gormx#103、gormx#104、gormx#105、gormx#106。

## [v0.11.2] — 2026-11-06

- build: 矩阵依赖地板对齐（维护轮 #233）

## [v0.11.1] — 2026-11-06

- fix(migrate): 脚本路径改 path.Join——embed.FS 的 fs.ValidPath 拒绝 "./mysql/x" 形态，Root=""/"." 的消费方此前会全量误报缺文件（消费方 kuboard-go/pkg/ddl 实录）

## [v0.11.0] — 2026-11-06

- feat: 新增 IntSoftDelete 整型软删除原语（deleted TINYINT 0=未删 1=已删）——查询/更新自动带 deleted=0，DELETE 自动改写 UPDATE；按主键删除自建 WHERE IN（与 gorm.SoftDeleteDeleteClause 语义一致）。feat: 新增 migrate 子包——「裸 SQL 脚本 + 单层平铺清单」建表/升级执行器：按 Target 选目录全量执行、SplitStatements（注释剥离+引号内分号不切分）、IsIgnorableError 幂等白名单、多列 ALTER ADD 失败自动拆单列逐条执行。来源：kuboard-go 已验证实现上移（matrix 决策 U1/U3）

## [v0.10.1] — 2026-11-06

- build: 矩阵依赖地板对齐（维护轮 #233）

## [v0.10.0] — 2026-11-06

- feat: 新增 `RunWithAdvisoryLock(ctx, db, key, fn)` 会话级咨询锁原语（[#79](https://github.com/oekit/gormx/issues/79)）——多副本迁移串行化的标准形态：拿锁→执行→解锁**钉在同一条连接**上（gorm `Connection()`，底层 `*sql.Conn`）。坑在连接池语义：`db.Exec("SELECT pg_advisory_lock(?)")` 与 `defer db.Exec("SELECT pg_advisory_unlock(?)")` 各自从池取连接，解锁落在另一条连接时是**空操作（不报错）**——锁随持锁连接归还池而泄漏（会话级锁不随归还释放），第二个副本的阻塞式拿锁等到持锁连接关闭（池 lifetime 分钟级）= 并发首启挂死；且 database/sql 池的 LIFO 空闲复用让单线程下解锁大概率落同连接——缺陷单进程内近乎不可见、无报错，只在多副本/并发 churn 下显形。契约：fn 结束（含 panic）必然解锁（defer 落在持锁连接）；拿锁阻塞可被 ctx 取消中断（未拿到锁的路径不解锁）；解锁走 `context.Background()`（收尾路径调用方 ctx 已取消也必须尽力解锁），解锁失败打 ERROR（锁泄漏到持锁连接关闭，池 lifetime 兜底，告警面应覆盖）；方言 postgres=pg_advisory_lock/unlock、mysql=GET_LOCK/RELEASE_LOCK（同键同坑一并收编，int64 键型共用）、其余（含 sqlite）返回类型化 `ErrAdvisoryLockUnsupported`——互斥诉求在不支持方言上显式报错，不静默退化成无锁执行

关联问题单：gormx#79。

## [v0.9.0] — 2026-11-05

- **BREAKING** fix(pool): `WithConnectionPool` 参数顺序对齐 database/sql 习惯（[#76](https://github.com/oekit/gormx/issues/76)）——原签名 idle 在前，与 `SetMaxOpenConns`/`SetMaxIdleConns` 的声明次序相反；传反**不报错**，只会静默把 MaxOpenConns 设成 idle 值（idle > open 的组合 database/sql 不报错，行为是 idle 永远达不成——bianque 装配点实录：想要 open=50/idle=10 实际 open=10/idle=50，靠调用点逐参命名防复发是宿主自律不是库防线）。新签名 `(maxOpenConns, maxIdleConns int, connMaxLifetime time.Duration)`；仓内调用点随迁（`OpenMySQL` 按 MySQLConfig 字段名装配、语义不变），0.x 阶段 breaking 可接受，迁移行：交换前两个实参位置
- fix(pool): 连接池 opt-in 静默退化守卫（[#75](https://github.com/oekit/gormx/issues/75)）——不传 `WithConnectionPool` ⇒ `MaxOpenConns=0`（database/sql 缺省无上限），多副本部署下必然打满 PG max_connections（bianque 上线首日事故实录：排查「连接数为什么不受控」才发现库内有配置能力、只是没被激活——「配置能力藏在库深处=没人知道它是什么、也就没人能发现它没生效」）。`Open` 对「MaxOpenConns 未设上限」的形态（含只传 WithConnMaxIdleTime 的部分配置）打一条 WARN（不拦行为——单实例开发/批处理可能确有意为之），Open 的 doc 顶部声明「池配置是 opt-in，不传=无上限，生产必须显式配置」
- feat(sqlite): 新增 `IsBusyErr(err) bool` 类型化判定原语（[#77](https://github.com/oekit/gormx/issues/77)）——宿主（bianque 全仓 5 处）此前各自 `strings.Contains("database is locked")`：错误文本判定是系统性技术债，且**已经失效过一半**——modernc 系驱动的 busy 文案是 "The database file is locked (SQLITE_BUSY)"，不含经典 mattn 子串 "database is locked"，宿主的文本匹配在新驱动下本就拦不住 BUSY。判定面单源在本库：①类型化路径（主）——modernc.org/sqlite 的 `*sqlite.Error` 实现 `Code() int`，SQLITE_BUSY=5/SQLITE_LOCKED=6，以**局部接口**断言而非 import 驱动包（根包零 sqlite 驱动链接的 #72 门禁不被破坏），码命中后仍要求文案带 SQLITE_BUSY/SQLITE_LOCKED 标记（防其他库同名 `Code()` 误报）；②文本兜底——经典 mattn 文案（大小写不敏感）。沿 errors 链遍历（gorm/驱动包装层透明）。补**真 busy 集成用例**（两条连接写同一文件库、A 持 IMMEDIATE 锁 B 立撞 BUSY——闭合「宿主无法在单测里构造真 busy」的等价性验证缺口；注：memory+cache=shared 模式构造不出——glebarez 驱动对 SQLITE_LOCKED_SHAREDCACHE 走 unlock_notify 内部无限等待，锁等待被驱动吞掉）

关联问题单：gormx#75、gormx#76、gormx#77。

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
