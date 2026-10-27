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

// IsUniqueViolation 判断给定 error 是否为唯一键冲突。
//
// 覆盖三种驱动的唯一冲突文案（**窄判据**：只匹配各家确切的唯一冲突措辞，
// 不加裸 "constraint failed" 之类的宽子串，避免把普通约束错误误判成唯一冲突）：
//
//   - sqlite（glebarez 纯 Go 驱动）："UNIQUE constraint failed: <表>.<列>"
//   - mysql："Error 1062: Duplicate entry …"
//   - pg："… duplicate key value violates unique constraint …"（含 23505）
//
// 注：sqlite 文案既不映射 gorm.ErrDuplicatedKey（未开 TranslateError 时），
// 也不含 mysql/pg 的措辞 ⇒ 缺这条分支时 sqlite 下的唯一冲突**判不出来**
// （消费方实证：pigeonbox/core 默认库就是 sqlite，见 oekit/gormx#30）。
func IsUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, gorm.ErrDuplicatedKey) {
		return true
	}
	msg := err.Error()
	return strings.Contains(msg, "UNIQUE constraint failed") || // sqlite
		strings.Contains(msg, "Duplicate entry") || // mysql 1062
		strings.Contains(msg, "duplicate key value") || // pg 文案
		strings.Contains(msg, "23505") // pg 错误码
}
