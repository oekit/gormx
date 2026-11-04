// 复现程序（#72 正对照）：只用 gormx 根包（MySQL/CRUD/事务）+ modernc.org/sqlite。
// 修复后两者可共存——根包不再带 glebarez 的驱动注册副作用。
package main

import (
	"fmt"

	_ "github.com/oekit/gormx"
	_ "modernc.org/sqlite"
)

func main() { fmt.Println("OK") }
