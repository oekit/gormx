package gormx

// 免 CGO 的 sqlite 入口（2026-10-28）。
//
// ## 为什么需要
//
// 本模块此前把 **CGO 驱动** `gorm.io/driver/sqlite`（包装 `mattn/go-sqlite3`）列为
// 直接依赖，而**主模块的生产代码一行都没用它**——它只出现在测试文件里。后果：
//
//  1. 本模块自己的测试套件**跑不了免 CGO 环境**（实测 `CGO_ENABLED=0 go test ./...`
//     全部失败：`Binary was compiled with 'CGO_ENABLED=0', go-sqlite3 requires cgo
//     to work. This is a stub`）⇒ 而 NAS 套件 / 容器静态构建（`CGO_ENABLED=0`）
//     正是本模块的消费方 ⇒ **消费方能构建的环境，库自己却不可测**；
//  2. 判据 `IsUniqueViolation` 的 sqlite 分支**声称**覆盖「glebarez 纯 Go 驱动」的
//     文案（见 transaction.go），而测试只跑 CGO 驱动 ⇒ **验证面 ≠ 承诺面**：纯 Go
//     驱动的错误文案若变化，库内测不出来。
//
// ## 处置
//
// 测试改用**纯 Go 驱动** `github.com/glebarez/sqlite`（与判据承诺的驱动同源，
// API 与 `gorm.io/driver/sqlite` 同构：`sqlite.Open(dsn) gorm.Dialector`）⇒
// ① 测试套件可在 `CGO_ENABLED=0` 下运行；② 判据的 sqlite 文案**由承诺的那个驱动
// 直接验证**；③ 主模块不再依赖任何 CGO 驱动。
//
// 另提供 `OpenPureGoSQLite` 供消费方直接打开 sqlite（免得各自拼驱动、拼出与判据
// 口径错配的组合）。

import (
	"fmt"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// OpenPureGoSQLite 用**纯 Go 驱动**（github.com/glebarez/sqlite，无 CGO）打开
// sqlite 数据库。
//
// 与 `OpenMySQL` 对称：同样接受连接池与 gorm 选项。选它而不是
// `gorm.io/driver/sqlite` 的原因：该驱动需要 CGO，而 NAS 套件 / 容器镜像通常要求
// `CGO_ENABLED=0` 的静态二进制；且本模块的 `IsUniqueViolation` 判据本就按纯 Go
// 驱动的错误文案匹配 ⇒ 入口与判据同源。
func OpenPureGoSQLite(dsn string, opts ...OpenOption) (*gorm.DB, error) {
	if dsn == "" {
		return nil, fmt.Errorf("sqlite dsn 不能为空")
	}
	return Open(sqlite.Open(dsn), opts...)
}
