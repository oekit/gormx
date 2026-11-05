package migrate

import (
	"strings"
	"testing"
	"testing/fstest"
)

// splitCases 固化 SplitStatements 的切分规则（与既有消费方 DdlService.splitStatements 同规则）。
func TestSplitStatements(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		wantN   int
		wantHas []string
	}{
		{"两句", "CREATE TABLE a (id INT);\nINSERT INTO a VALUES (1);\n", 2, []string{"CREATE TABLE a", "INSERT INTO a"}},
		{"注释剥离", "-- 建表\nCREATE TABLE a (id INT);\n", 1, []string{"CREATE TABLE"}},
		{"块注释剥离", "/* header */ CREATE TABLE a (id INT);\n", 1, []string{"CREATE TABLE"}},
		{"引号内分号", "INSERT INTO a VALUES ('x;y');\n", 1, []string{"x;y"}},
		{"反引号", "CREATE TABLE `t;x` (id INT);\n", 1, []string{"`t;x`"}},
		{"转义引号", "INSERT INTO a VALUES ('it\\'s');\n", 1, []string{"it\\'s"}},
		{"尾部无分号", "CREATE TABLE a (id INT)", 1, []string{"CREATE TABLE"}},
		{"空语句丢弃", ";\n;\nCREATE TABLE a (id INT);\n", 1, []string{"CREATE TABLE"}},
	}
	for _, c := range cases {
		got := SplitStatements(c.in)
		if len(got) != c.wantN {
			t.Fatalf("%s: SplitStatements() got %d stmts, want %d: %#v", c.name, len(got), c.wantN, got)
		}
		for _, want := range c.wantHas {
			found := false
			for _, s := range got {
				if strings.Contains(s, want) {
					found = true
					break
				}
			}
			if !found {
				t.Fatalf("%s: no stmt contains %q: %#v", c.name, want, got)
			}
		}
	}
}

func TestIsIgnorableError(t *testing.T) {
	ignorable := []string{
		"Error 1062: Duplicate entry 'x' for key 'uk'",
		"Error 1060: Duplicate column name 'c'",
		"Error 1061: Duplicate key name 'i'",
		"Error 1091: check that column/key exists",
		"Error 1091: Unknown index 'i'",
		`ERROR: relation "a" already exists (SQLSTATE 42P07)`,
		"duplicate table",
		"already been created",
	}
	for _, msg := range ignorable {
		if !IsIgnorableError(errString(msg)) {
			t.Fatalf("IsIgnorableError(%q) = false, want true", msg)
		}
	}
	fatal := []string{
		"syntax error near 'x'",
		"connection refused",
		"Access denied for user",
	}
	for _, msg := range fatal {
		if IsIgnorableError(errString(msg)) {
			t.Fatalf("IsIgnorableError(%q) = true, want false", msg)
		}
	}
}

type errString string

func (e errString) Error() string { return string(e) }

// testFS 构造「单层平铺清单 + 按 Target 选目录」的脚本源。
func testFS() fstest.MapFS {
	return fstest.MapFS{
		"ddl/mysql/01-install.sql": &fstest.MapFile{Data: []byte(
			"CREATE TABLE t_a (id INT PRIMARY KEY);\n" +
				"INSERT INTO t_a VALUES (1);\n")},
		"ddl/postgre/01-install.sql": &fstest.MapFile{Data: []byte(
			"CREATE TABLE t_a (id INT PRIMARY KEY);\n" +
				"INSERT INTO t_a VALUES (1);\n")},
		"ddl/mysql/02-upgrade.sql": &fstest.MapFile{Data: []byte(
			"CREATE TABLE t_a (id INT PRIMARY KEY);\n" + // 幂等重复建表
				"ALTER TABLE t_a ADD COLUMN c1 INT;\n" +
				"ALTER TABLE t_a ADD COLUMN c1 INT;\n" + // c1 已存在 ⇒ 幂等跳过
				"INSERT INTO t_a (id) VALUES (2);\n")},
		"ddl/postgre/02-upgrade.sql": &fstest.MapFile{Data: []byte(
			"CREATE TABLE t_a (id INT PRIMARY KEY);\n" +
				"ALTER TABLE t_a ADD c1 INT;\n" +
				"ALTER TABLE t_a ADD c1 INT;\n" + // PG 幂等跳过
				"INSERT INTO t_a (id) VALUES (2);\n")},
	}
}

// Run 的端到端形态：install → upgrade 全量执行；幂等错误跳过；多列 ALTER 拆分。
func TestRunExecutesInstallAndUpgrade(t *testing.T) {
	db := openMigrateTestDB(t)
	opts := Options{
		Target:  "mysql",
		Install: []string{"01-install.sql"},
		Upgrade: []string{"02-upgrade.sql"},
		FS:      testFS(),
		Root:    "ddl",
	}
	if err := Run(db, opts); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	// 全部列都要在：升级语义 = 「幂等跳过不误删、数据完整」
	for _, col := range []string{"id", "c1"} {
		var n int
		if err := db.Raw("SELECT COUNT(" + col + ") FROM t_a").Scan(&n).Error; err != nil {
			t.Fatalf("check col %s: %v", col, err)
		}
	}
	var rows int
	if err := db.Raw("SELECT COUNT(*) FROM t_a").Scan(&rows).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if rows != 2 {
		t.Fatalf("t_a rows = %d, want 2", rows)
	}
}

// Target 选目录：同一份清单，postgre 目录的脚本只命中 postgre Target。
func TestRunSelectsTargetDir(t *testing.T) {
	db := openMigrateTestDB(t)
	opts := Options{
		Target:  "postgre",
		Install: []string{"01-install.sql"},
		FS:      testFS(),
		Root:    "ddl",
	}
	if err := Run(db, opts); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
}

// embed.FS 形态的消费方：Root="" 或 "."（路径前缀）都不得误报缺文件——
// fs.ValidPath 拒绝 "./mysql/x"，必须经 path.Join 归一。
func TestRunRootVariants(t *testing.T) {
	flatFS := fstest.MapFS{
		"mysql/01-install.sql": &fstest.MapFile{Data: []byte("CREATE TABLE t_root (id INT PRIMARY KEY);\n")},
	}
	for _, root := range []string{"", "."} {
		db := openMigrateTestDB(t)
		opts := Options{
			Target:  "mysql",
			Install: []string{"01-install.sql"},
			FS:      flatFS,
			Root:    root,
		}
		if err := Run(db, opts); err != nil {
			t.Fatalf("Run(Root=%q) error = %v", root, err)
		}
	}
}

// 缺脚本文件必须显式报错（清单与目录内容失配是部署错误，静默跳过=漂移）。
func TestRunMissingScriptFails(t *testing.T) {
	db := openMigrateTestDB(t)
	opts := Options{
		Target:  "mysql",
		Install: []string{"missing.sql"},
		FS:      testFS(),
		Root:    "ddl",
	}
	if err := Run(db, opts); err == nil {
		t.Fatal("Run() with missing script must fail")
	}
}

// 不可忽略错误必须显式报错并携带语句上下文。
func TestRunFatalErrorFails(t *testing.T) {
	db := openMigrateTestDB(t)
	opts := Options{
		Target:  "mysql",
		Install: []string{"bad.sql"},
		FS: fstest.MapFS{
			"ddl/mysql/bad.sql": &fstest.MapFile{Data: []byte("CREATE TABLE t_a (id INT PRIMARY KEY);\nTHIS IS NOT SQL;\n")},
		},
		Root: "ddl",
	}
	err := Run(db, opts)
	if err == nil {
		t.Fatal("Run() with invalid SQL must fail")
	}
	if !strings.Contains(err.Error(), "bad.sql") {
		t.Fatalf("error must carry script path, got: %v", err)
	}
}
