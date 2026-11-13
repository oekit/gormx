// Package sqlitedrv 是 gormx 的 sqlite 入口子包（纯 Go 驱动，免 CGO）。
//
// # 为什么单独成包（oekit/gormx#72）
//
// 底层驱动 github.com/glebarez/sqlite（经 github.com/glebarez/go-sqlite）在
// init() 里向 database/sql 注册驱动名 "sqlite"，而 modernc.org/sqlite 同样在
// init() 注册 "sqlite" ⇒ **同一二进制同时链接两者，init 阶段直接 panic**：
//
//	panic: sql: Register called twice for driver "sqlite"
//
// 此前入口 OpenPureGoSQLite 住在根包 gormx ⇒ 只要用 gormx（哪怕只用
// MySQL/CRUD/事务），glebarez 就进依赖图、init 就注册 ⇒ 与任何直接使用
// modernc.org/sqlite 的库（agent-memory 等）组合启动即炸（消费方实证：
// code-review-service，#72）。
//
// 现在 glebarez **只被本子包引用**：根包 gormx、crud、gormrepo 的生产代码
// 零 sqlite 驱动链接（有门禁测试 deps_sqlite_link_test.go 钉住），只有显式
// import 本包的二进制才会带上纯 Go sqlite 驱动。
//
// # 边界（如实声明）
//
// 本包与 modernc.org/sqlite 仍**不可同二进制共存**——两者的 init 都注册
// "sqlite"，这是驱动上游的行为，本包无法消除。既用本包入口又直接
// import modernc.org/sqlite 的组合依旧会 panic；需要共存时二选一：
// 用本包（glebarez 系）或直接用 modernc（不要再引本包）。
// mattn 系（gorm.io/driver/sqlite）注册的是 "sqlite3"，与两者不冲突。
package sqlitedrv

import (
	"fmt"

	"github.com/glebarez/sqlite"
	"github.com/oekit/gormx"
	"gorm.io/gorm"
)

// Open 用纯 Go 驱动（github.com/glebarez/sqlite，免 CGO）打开 sqlite 数据库。
//
// 与 gormx.OpenMySQL 对称：同样接受连接池与 gorm 选项（gormx.OpenOption）。
// 选它而不是 gorm.io/driver/sqlite 的原因：该驱动需要 CGO，而 NAS 套件 /
// 容器镜像通常要求 CGO_ENABLED=0 的静态二进制；且 gormx.IsUniqueViolation
// 判据本就按纯 Go 驱动的错误文案匹配 ⇒ 入口与判据同源。
func Open(dsn string, opts ...gormx.OpenOption) (*gorm.DB, error) {
	if dsn == "" {
		return nil, fmt.Errorf("sqlite dsn 不能为空")
	}
	return gormx.Open(sqlite.Open(dsn), opts...)
}
