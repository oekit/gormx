package gormx

import (
	"errors"
	"strings"
)

// SQLite busy（SQLITE_BUSY / SQLITE_LOCKED）的类型化判定原语（#77）。
//
// 背景：SQLite 写锁竞态是宿主高发面（多协程/多副本写同一库），此前宿主被迫
// 在业务层各自 strings.Contains("database is locked")——错误文本判定是系统性
// 技术债：驱动文案一改就静默失效（且已经失效过一半：modernc 系驱动的 busy
// 文案是 "The database file is locked (SQLITE_BUSY)"，**不含**经典 mattn 文案
// "database is locked"——宿主的文本匹配在新驱动下本就拦不住 BUSY）。方言判定
// 是 repo/工具层职责，宿主业务层 import sqlite 驱动做类型断言是架构倒退。
//
// 判定面单源在本库：
//  1. 类型化路径（主）：modernc.org/sqlite 的 *sqlite.Error（glebarez/sqlite
//     GORM 驱动的底层）实现 Code() int，SQLITE_BUSY=5 / SQLITE_LOCKED=6。
//     以**局部接口**断言而非 import 驱动包——根包零 sqlite 驱动链接的契约
//     （gormx#72 门禁）不被破坏。码命中后仍要求文案带 SQLITE_BUSY/SQLITE_LOCKED
//     标记——防其他库的同名 Code() 方法误报。
//  2. 文本兜底：已知稳定的经典文案（cgo mattn 形态），大小写不敏感。

// sqliteCoder 是 modernc.org/sqlite *sqlite.Error 的局部接口投影。
// 不 import 驱动包（gormx#72：根包零 sqlite 驱动链接），断言到「有 Code() int」
// 再配合文案标记判定。
type sqliteCoder interface {
	Code() int
}

// SQLite 结果码（与 modernc.org/sqlite/lib 的常量同值；不 import 驱动故本地
// 具名，注释锁定来源与取值）。
const (
	sqliteBusy   = 5 // SQLITE_BUSY
	sqliteLocked = 6 // SQLITE_LOCKED
)

// IsBusyErr 判定 err 是否 SQLite busy 类错误（库/表被锁，可退避重试）。
// 沿 errors 链遍历（gorm 与驱动的包装层透明）。err 为 nil 返回 false。
//
// 典型用法（配合 retry 退避，busy 是瞬态锁竞态、重试通常即刻成功）：
//
//	err := retry.Do(ctx, retry.Config{Attempts: 5, Delay: 50 * time.Millisecond,
//	    Exponential: true, MaxDelay: time.Second}, func(int) error {
//	        return tx.Create(&row).Error
//	})
//	// 或门控：if gormx.IsBusyErr(err) { ... 退避后重试 ... }
func IsBusyErr(err error) bool {
	if err == nil {
		return false
	}
	// 类型化路径：码 + 文案标记双条件（防同名 Code() 误报）
	var coder sqliteCoder
	if errors.As(err, &coder) {
		switch coder.Code() {
		case sqliteBusy, sqliteLocked:
			msg := err.Error()
			if strings.Contains(msg, "SQLITE_BUSY") || strings.Contains(msg, "SQLITE_LOCKED") {
				return true
			}
		}
	}
	// 文本兜底：经典 mattn/驱动包装文案（大小写不敏感）
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "database is locked") ||
		strings.Contains(msg, "database table is locked")
}
