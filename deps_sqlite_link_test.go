package gormx

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// sqliteDriverImports 是**不允许出现在生产代码**里的 sqlite 驱动 import（#72）。
//
//	github.com/glebarez/sqlite     —— gorm 纯 Go dialector（init 注册 "sqlite"）
//	github.com/glebarez/go-sqlite  —— 其底层 database/sql 驱动（init 注册 "sqlite"）
//	modernc.org/sqlite             —— 另一家纯 Go 驱动（init 也注册 "sqlite"）
//	gorm.io/driver/sqlite          —— CGO dialector（包装 mattn/go-sqlite3）
//	github.com/mattn/go-sqlite3    —— CGO 实现本体（init 注册 "sqlite3"）
//
// 缘由（oekit/gormx#72）：glebarez 系与 modernc.org/sqlite 都在 init() 里
// sql.Register("sqlite", ...) ⇒ 同一二进制链接两者启动即
// panic: sql: Register called twice for driver "sqlite"。入口 OpenPureGoSQLite
// 曾住根包 ⇒ 只要用 gormx（哪怕只用 MySQL）就带上 glebarez 的注册副作用，
// 与任何直接用 modernc.org/sqlite 的库组合即炸（消费方实证 code-review-service）。
//
// 现口径：**根包 gormx、crud、gormrepo 的生产代码零 sqlite 驱动链接**，
// 纯 Go sqlite 入口只住 sqlitedrv 子包（显式 import 才带上驱动）。
// 本门禁是这条契约的可执行形态（AST 静态断言，不依赖 go list / 网络）。
var sqliteDriverImports = []string{
	"github.com/glebarez/sqlite",
	"github.com/glebarez/go-sqlite",
	"modernc.org/sqlite",
	"gorm.io/driver/sqlite",
	"github.com/mattn/go-sqlite3",
}

// corePkgs 是必须保持「零 sqlite 驱动链接」的生产包（相对模块根的目录）。
// sqlitedrv **不在**其中——它是唯一的合法 sqlite 驱动引用方（正对照见
// TestSQLiteDriverImportOnlyInSqlitedrv）。
var corePkgs = []string{".", "crud", "gormrepo"}

// TestNoSQLiteDriverImportInCorePackages 断言核心包生产代码零 sqlite 驱动 import。
//
// 只扫**非测试**的 .go 文件：测试用 glebarez 开内存库不进消费方二进制，
// 不构成链接污染。
func TestNoSQLiteDriverImportInCorePackages(t *testing.T) {
	root := moduleRoot(t)
	for _, pkg := range corePkgs {
		dir := root
		if pkg != "." {
			dir = filepath.Join(root, pkg)
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("读目录 %s: %v", dir, err)
		}
		for _, e := range entries {
			name := e.Name()
			if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				continue
			}
			path := filepath.Join(dir, name)
			for _, imp := range parseImports(t, path) {
				for _, banned := range sqliteDriverImports {
					if imp == banned || strings.HasPrefix(imp, banned+"/") {
						t.Errorf("%s/%s 生产代码 import 了 sqlite 驱动 %q"+
							"——#72 口径：sqlite 驱动只允许住 sqlitedrv 子包"+
							"（glebarez 系与 modernc.org/sqlite 都在 init 注册驱动名 \"sqlite\"，"+
							"核心包带上它们会让消费方与 modernc 系组合启动即 panic）", pkg, name, imp)
					}
				}
			}
		}
	}
}

// TestSQLiteDriverImportOnlyInSqlitedrv 是正对照：sqlitedrv 子包**确实**引用着
// 纯 Go 驱动（被测物存在且真被接线）——否则上面的门禁会因「哪都没有」而空转通过。
func TestSQLiteDriverImportOnlyInSqlitedrv(t *testing.T) {
	root := moduleRoot(t)
	path := filepath.Join(root, "sqlitedrv", "sqlitedrv.go")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("sqlitedrv/sqlitedrv.go 不存在——纯 Go sqlite 入口没有落点: %v", err)
	}
	found := false
	for _, imp := range parseImports(t, path) {
		if imp == "github.com/glebarez/sqlite" {
			found = true
		}
	}
	if !found {
		t.Error("sqlitedrv/sqlitedrv.go 应 import github.com/glebarez/sqlite（入口与 IsUniqueViolation 判据同源的承诺落点）")
	}
}

// parseImports 返回文件全部 import 路径（含别名 import）。
func parseImports(t *testing.T, path string) []string {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
	if err != nil {
		t.Fatalf("解析 %s: %v", path, err)
	}
	out := make([]string, 0, len(f.Imports))
	for _, imp := range f.Imports {
		out = append(out, strings.Trim(imp.Path.Value, "\""))
	}
	return out
}
