package gormx

import (
	"context"
	"errors"
	"strings"

	"gorm.io/gorm"
)

// TXFunc 是在事务中执行的函数类型，接收事务内的 *gorm.DB 并返回 error。
type TXFunc func(tx *gorm.DB) error

// InTransactionContext 在带有 context 的数据库事务中执行指定函数。
func InTransactionContext(ctx context.Context, db *gorm.DB, fn TXFunc) error {
	return db.WithContext(ctx).Transaction(fn)
}

// pgSQLStater 是 *pgconn.PgError 的局部接口投影（口径同 migrate 包）：
// 不 import pgx（本模块零 pg 驱动链接），SQLState() 方法签名与 pgconn.PgError
// 一致，errors.As 沿错误链命中（gorm/包装层透明）。
type pgSQLStater interface {
	SQLState() string
}

// pgUniqueViolationSQLState 是 PostgreSQL unique_violation 的 SQLSTATE
// （协议层契约，不受文案漂移影响）。
const pgUniqueViolationSQLState = "23505"

// IsUniqueViolation 判断给定 error 是否为唯一键冲突。
//
// 覆盖三种驱动的唯一冲突（**窄判据**：只匹配各家确切的唯一冲突措辞与协议层
// 错误码，不加裸 "constraint failed" 之类的宽子串，避免把普通约束错误误判成
// 唯一冲突）：
//
//   - sqlite（glebarez 纯 Go 驱动）："UNIQUE constraint failed: <表>.<列>"
//   - mysql："Error 1062: Duplicate entry …"
//   - pg："… duplicate key value violates unique constraint …"（文案）；
//     错误码走**类型化字段判定**——errors.As 命中 SQLState() 投影后比对
//     SQLSTATE == 23505（#163）。旧实现裸子串匹配 "23505" 恰是宽判据：
//     错误文本里的五位数字是常态（列号 "at column 23505"、端口
//     "dial tcp …:23505"、字节数 "23505 bytes"），命中即把语法/网络错误
//     误判成唯一冲突——与本注释承诺的「窄判据」相反。裸数字分支已删，
//     只留 pgconn.Error() 的码邻接形态 "(SQLSTATE 23505)" 作文案兜底。
//
// 注：sqlite 文案既不映射 gorm.ErrDuplicatedKey（未开 TranslateError 时），
// 也不含 mysql/pg 的措辞 ⇒ 缺这条分支时 sqlite 下的唯一冲突**判不出来**
// （消费方实证：pigeonbox/core 默认库就是 sqlite，见 oekit/gormx#30）。
//
// sqlite 分支由**纯 Go 驱动**（github.com/glebarez/sqlite）直接验证：2026-10-28
// 起本模块测试改用该驱动（此前用 CGO 驱动 ⇒ 验证面 ≠ 承诺面，且免 CGO 环境
// 跑不了本模块测试，见 oekit/gormx#36）。纯 Go 驱动的实际文案是
// "constraint failed: UNIQUE constraint failed: <表>.<列> (2067)"——含上述子串。
func IsUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, gorm.ErrDuplicatedKey) {
		return true
	}
	// 类型化字段判定（#163）：pgconn.PgError 的 SQLSTATE == 23505 才算 pg
	// 唯一冲突——码是协议层契约，且零误命中面（不看文案里的数字）。
	var pg pgSQLStater
	if errors.As(err, &pg) && pg.SQLState() == pgUniqueViolationSQLState {
		return true
	}
	msg := err.Error()
	return strings.Contains(msg, "UNIQUE constraint failed") || // sqlite
		strings.Contains(msg, "Duplicate entry") || // mysql 1062
		strings.Contains(msg, "duplicate key value") || // pg 文案
		strings.Contains(msg, "(SQLSTATE 23505)") // pgconn.Error() 的码邻接形态（带上下文，非裸数字）
}
