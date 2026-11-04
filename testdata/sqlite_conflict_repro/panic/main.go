// 复现程序（#72 负对照）：gormx/sqlitedrv（glebarez 系）+ modernc.org/sqlite。
// 两者 init 都注册驱动名 "sqlite" ⇒ 启动即 panic——这是驱动上游行为，
// 本包无法消除（边界如实声明），此程序固化该边界，别把它当回归。
package main

import (
	_ "github.com/oekit/gormx/sqlitedrv"
	_ "modernc.org/sqlite"
)

func main() {}
