// Package sqlitedrv 是 gormx 的 **opt-in 纯 Go sqlite 入口**。
//
// ## 为什么 sqlite 入口必须是独立子包（oekit/gormx#72）
//
// 纯 Go 驱动 `github.com/glebarez/sqlite` 在 `init()` 里向 `database/sql`
// 注册驱动名 **"sqlite"**——这是**链接级副作用**：只要二进制的 import 图里
// 有它，启动就注册。本入口此前放在核心包 `gormx` 里，导致**每个消费方**
// （哪怕只用 MySQL）都被强制带上：
//
//  1. **驱动名冲突，运行时才炸**：消费方再链接任何注册同名驱动的实现
//     （如 `modernc.org/sqlite`，经 agent-memory/storage/sqlite 等常见链路引入）
//     ⇒ init 顺序即 `panic: sql: Register called twice for driver sqlite`，
//     **不可 recover**。实际炸点：code-review-service 同时用 gormx（MySQL/GORM）
//     与 agent-memory 的 sqlite 存储，自第七波依赖对齐后 main 二进制启动即 panic；
//  2. **构建成本摊给所有人**：glebarez 的传递依赖（`modernc.org/sqlite` →
//     `modernc.org/libc`）体量大，实测只用 MySQL 的消费方也付冷构建 +5s。
//
// v0.8.0 起 `OpenPureGoSQLite` 迁入本子包（⚠️ breaking：核心包不再导出该入口，
// 迁移＝改 import 为 `github.com/oekit/gormx/sqlitedrv`，调用改为
// `sqlitedrv.OpenPureGoSQLite`，签名不变）。此后**核心包（`./`、`crud/`、
// `gormrepo/`）零 sqlite 链接**：不 import 本子包的二进制既不注册任何驱动，
// 也不付 sqlite 驱动的构建成本。
//
// 「核心包不得链接任何 sqlite 驱动」由 `deps_sqlite_link_test.go` 以 AST 判 import
// 强制（不靠文本匹配），退回旧写法（把入口挪回核心包）自测必红。
//
// ## 仍须注意：注册者必须唯一
//
// 显式选用本子包的消费方，二进制里就有了 glebarez 的 "sqlite" 注册——此时再
// 直接 import `modernc.org/sqlite` 等同名注册者仍会冲突。gormx 只保证
// **不替你做决定**；驱动注册者唯一性由选用 sqlite 的消费方自行保证。
// mattn 系（`gorm.io/driver/sqlite`）注册的是 "sqlite3"，与 glebarez/modernc 不冲突。
package sqlitedrv
