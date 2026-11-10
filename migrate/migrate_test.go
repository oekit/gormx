package migrate

import (
	"bytes"
	"errors"
	"fmt"
	"log"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/go-sql-driver/mysql"
	"github.com/oekit/gormx"
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

// fakePgError 按 *pgconn.PgError 的公开形状镜像：SQLState() 方法（pgconn 的
// 错误码经该方法暴露，局部接口断言正是锚定它）。文案用非英语（模拟 PG
// lc_messages 本地化后英文兜底 needles 全部失配的形态），断言只有类型化
// 路径能命中。
type fakePgError struct{ code, msg string }

func (e *fakePgError) Error() string    { return "FEHLER: " + e.msg + " (SQLSTATE " + e.code + ")" }
func (e *fakePgError) SQLState() string { return e.code }

// TestIsIgnorableError_MySQLTypedPath 真值面（#96）：真实 *mysql.MySQLError
// （go-sql-driver 已在本模块依赖图内，测试直接构造驱动错误值，无需服务器）。
// Message 用非英文模拟文案漂移（驱动升级/本地化/proxy 改写）——文案兜底
// needles 全部失配，命中的只能是类型化的 Number 字段路径。
func TestIsIgnorableError_MySQLTypedPath(t *testing.T) {
	ignorable := []uint16{1050, 1060, 1061, 1062, 1091}
	for _, n := range ignorable {
		err := &mysql.MySQLError{Number: n, Message: "lokalisierte Fehlermeldung"}
		if !IsIgnorableError(err) {
			t.Errorf("MySQL 错误号 %d 应经类型化路径判为可忽略", n)
		}
	}
	// 负例：致命错误号不得因文案巧合被放过（数字不匹配任何 needle）
	for _, n := range []uint16{1064 /*syntax*/, 1146 /*no table*/, 1045 /*access denied*/} {
		if IsIgnorableError(&mysql.MySQLError{Number: n, Message: "schwerer Fehler"}) {
			t.Errorf("MySQL 错误号 %d 不应判为可忽略", n)
		}
	}
	// 错误链穿透：gorm/调用方的 %w 包装层
	wrapped := fmt.Errorf("exec ddl: %w", &mysql.MySQLError{Number: 1062, Message: "x"})
	if !IsIgnorableError(wrapped) {
		t.Error("errors 链上的 *MySQLError 应被命中")
	}
}

// TestIsIgnorableError_PGTypedPath PG SQLSTATE 类型化路径（#96）：
// 白名单码全命中、非白名单码（语法错/外键/权限）不误判。
func TestIsIgnorableError_PGTypedPath(t *testing.T) {
	for _, code := range []string{"42P07", "42P01", "42701", "42710", "23505"} {
		if !IsIgnorableError(&fakePgError{code: code, msg: "lokalisierte Meldung"}) {
			t.Errorf("SQLSTATE %s 应经类型化路径判为可忽略", code)
		}
	}
	for _, code := range []string{"42601" /*syntax*/, "23503" /*fk*/, "42501" /*priv*/} {
		if IsIgnorableError(&fakePgError{code: code, msg: "schwerer Fehler"}) {
			t.Errorf("SQLSTATE %s 不应判为可忽略", code)
		}
	}
	// 包装链穿透
	wrapped := fmt.Errorf("migrate: %w", &fakePgError{code: "42P07", msg: "x"})
	if !IsIgnorableError(wrapped) {
		t.Error("errors 链上的 SQLStater 应被命中")
	}
}

// TestIsIgnorableError_TypedMissDoesNotLeakIntoText 类型未命中时不得提前
// 返回 false 绕过文案兜底——兜底是最后防线（sqlite 文案等仍依赖它）。
func TestIsIgnorableError_TypedMissDoesNotLeakIntoText(t *testing.T) {
	// 实现了 SQLState() 但码不在白名单，同时文案命中 needle：
	// 兜底必须仍然生效。
	err := &fakePgError{code: "99999", msg: "relation already exists (hypothetical)"}
	if !IsIgnorableError(err) {
		t.Error("类型化未命中不应吞掉文案兜底")
	}
}

// captureMigrateLog 重定向标准库 log 输出（logx Delegating 缺省后端），
// 捕获 migrate 的 WARN 文案做断言。
func captureMigrateLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	buf := &bytes.Buffer{}
	prev := log.Writer()
	log.SetOutput(buf)
	t.Cleanup(func() { log.SetOutput(prev) })
	return buf
}

// TestRunDelimiterScriptWarnsRootCause（#104）：DELIMITER 脚本超出切分器
// 能力（过程体分号被逐条拆出、必然语法报错）。fail-loud 之前必须先打
// WARN 指明根因——否则消费方看到的是过程体第一句的语法错误，不会指向
// 切分器局限。
func TestRunDelimiterScriptWarnsRootCause(t *testing.T) {
	db := openMigrateTestDB(t)
	buf := captureMigrateLog(t)

	script := "DELIMITER $$\n" +
		"CREATE PROCEDURE p() BEGIN SELECT 1; SELECT 2; END$$\n" +
		"DELIMITER ;\n"
	opts := Options{
		Target:  "mysql",
		Install: []string{"proc.sql"},
		FS:      fstest.MapFS{"ddl/mysql/proc.sql": &fstest.MapFile{Data: []byte(script)}},
		Root:    "ddl",
	}
	// sqlite 下碎片执行报语法错误是预期（fail-loud）；断言的是 WARN 根因
	// 提示先于/伴随失败出现。
	_ = Run(db, opts)
	if !strings.Contains(buf.String(), "DELIMITER") || !strings.Contains(buf.String(), "不支持 DELIMITER") {
		t.Errorf("DELIMITER 脚本应打根因 WARN, got: %q", buf.String())
	}
	if !strings.Contains(buf.String(), "proc.sql") {
		t.Errorf("WARN 应携带脚本路径, got: %q", buf.String())
	}
}

// TestSplitStatementsDelimiterNoStateMachine 固化当前行为（#104 文档口径）：
// DELIMITER 指令不切状态，过程体分号被当作语句终止符——这是声明出的局限，
// 不是隐性契约；将来若实现状态机，本用例应同步改写。
func TestSplitStatementsDelimiterNoStateMachine(t *testing.T) {
	script := "DELIMITER $$\nCREATE PROCEDURE p() BEGIN SELECT 1; SELECT 2; END$$\nDELIMITER ;\n"
	got := SplitStatements(script)
	if len(got) < 3 {
		t.Fatalf("无状态机时过程体应被分号切碎（当前声明行为）, got %#v", got)
	}
	first := strings.Fields(strings.TrimSpace(got[0]))
	if len(first) == 0 || !strings.EqualFold(first[0], "DELIMITER") {
		t.Errorf("首条碎片应以 DELIMITER 开头, got %#v", got[0])
	}
}

// ---- #164：execSplitAlters 的非 ADD 守卫（head 也入守卫） ----

// TestExecSplitAlters_HeadClauseNotRideAlong 回归（#164）：head（首个 ADD
// 之前）的非 ADD 子句不得携带进 ADD 条目的拆分语句。旧实现守卫只查 parts
// 不查 head——携带 head 的拆分语句整条原子失败、被 IsIgnorableError 静默
// continue ⇒ 要加的列全被吞掉，execSplitAlters 却返回 (true, nil) 报
// 「已拆分执行」（迁移绿、schema 永久缺列）。
// 修复后 head 子句逐条**单独先执行**（失败 ignorable 跳过），ADD 条目裸拆。
func TestExecSplitAlters_HeadClauseNotRideAlong(t *testing.T) {
	db := openMigrateTestDB(t)
	if err := db.Exec("CREATE TABLE t64 (c INT, b INT)").Error; err != nil {
		t.Fatalf("create: %v", err)
	}
	// head = RENAME COLUMN c TO b：目标名 b 已存在 ⇒ 单独执行报
	// "duplicate column name: b"（ignorable），不得连坐 ADD 条目
	stmt := "ALTER TABLE t64 RENAME COLUMN c TO b, ADD COLUMN a1 INT, ADD COLUMN a2 INT"
	ok, err := execSplitAlters(db, stmt)
	if !ok || err != nil {
		t.Fatalf("execSplitAlters = (%v, %v), want (true, nil)", ok, err)
	}
	// 要加的列必须落库（修复前 head 连坐 ⇒ a1、a2 被静默吞掉）
	for _, col := range []string{"a1", "a2"} {
		var n int
		if err := db.Raw("SELECT COUNT(" + col + ") FROM t64").Scan(&n).Error; err != nil {
			t.Fatalf("列 %s 未加上（#164 复发：head 的 ignorable 失败吞掉 ADD 条目）：%v", col, err)
		}
	}
	// head 子句 ignorable 失败 ⇒ 只跳过它自己（c 列名不变、b 列不被改名）
	var n int
	if err := db.Raw("SELECT COUNT(c) FROM t64").Scan(&n).Error; err != nil {
		t.Fatalf("head 子句 ignorable 失败应跳过、c 列应仍在：%v", err)
	}
}

// TestExecSplitAlters_HeadClauseExecutesFirst head 子句单独先执行的正向面：
// head 非 ADD 子句自身可执行时照常生效，ADD 条目随后落库（与旧行为的差异：
// 旧实现把 head 拼进每条拆分语句 ⇒ sqlite 下直接语法错、MySQL 下 head 失败
// 连坐）。
func TestExecSplitAlters_HeadClauseExecutesFirst(t *testing.T) {
	db := openMigrateTestDB(t)
	if err := db.Exec("CREATE TABLE t64b (c INT, x INT)").Error; err != nil {
		t.Fatalf("create: %v", err)
	}
	stmt := "ALTER TABLE t64b DROP COLUMN c, ADD COLUMN a1 INT, ADD COLUMN a2 INT"
	ok, err := execSplitAlters(db, stmt)
	if !ok || err != nil {
		t.Fatalf("execSplitAlters = (%v, %v), want (true, nil)", ok, err)
	}
	var n int
	if err := db.Raw("SELECT COUNT(c) FROM t64b").Scan(&n).Error; err == nil {
		t.Error("head 子句 DROP COLUMN c 应已执行（c 列应已删除）")
	}
	for _, col := range []string{"a1", "a2"} {
		if err := db.Raw("SELECT COUNT(" + col + ") FROM t64b").Scan(&n).Error; err != nil {
			t.Fatalf("列 %s 未加上：%v", col, err)
		}
	}
}

// TestExecSplitAlters_MultiAddBareSplit 基线钉住：纯多列 ADD 的拆分行为不变
// （每条 ADD 一个 ALTER，全部落库）。
func TestExecSplitAlters_MultiAddBareSplit(t *testing.T) {
	db := openMigrateTestDB(t)
	if err := db.Exec("CREATE TABLE t64c (x INT)").Error; err != nil {
		t.Fatalf("create: %v", err)
	}
	ok, err := execSplitAlters(db, "ALTER TABLE t64c ADD COLUMN a1 INT, ADD COLUMN a2 INT")
	if !ok || err != nil {
		t.Fatalf("execSplitAlters = (%v, %v), want (true, nil)", ok, err)
	}
	var n int
	for _, col := range []string{"a1", "a2"} {
		if err := db.Raw("SELECT COUNT(" + col + ") FROM t64c").Scan(&n).Error; err != nil {
			t.Fatalf("列 %s 未加上：%v", col, err)
		}
	}
}

// TestExecSplitAlters_TailNonAddGuard tail 守卫契约不变：ADD 条目**之间**
// 混入非 ADD 子句时不做拆分（返回 false，走整条语句的幂等跳过路径）。
// 顺带收紧为「守卫命中即零执行」：旧实现在守卫命中前已逐条执行了前面的
// ADD 条目（返回 false 却已落库，与「不做拆分」自相矛盾）。
func TestExecSplitAlters_TailNonAddGuard(t *testing.T) {
	db := openMigrateTestDB(t)
	if err := db.Exec("CREATE TABLE t64d (c INT, x INT)").Error; err != nil {
		t.Fatalf("create: %v", err)
	}
	ok, err := execSplitAlters(db, "ALTER TABLE t64d ADD COLUMN a1 INT, RENAME COLUMN c TO x")
	if ok || err != nil {
		t.Fatalf("tail 混入非 ADD 子句应拒绝拆分，got (%v, %v)", ok, err)
	}
	var n int
	if err := db.Raw("SELECT COUNT(a1) FROM t64d").Scan(&n).Error; err == nil {
		t.Error("不做拆分时不应执行任何子句")
	}
}

// TestSplitAlterTable 形状识别面：前缀/表名/子句切分，识别失败一律 ok=false
// （存疑不拆）。
func TestSplitAlterTable(t *testing.T) {
	cases := []struct {
		name      string
		in        string
		wantOK    bool
		wantPfx   string
		wantN     int
		wantFirst string
	}{
		{"裸表名", "ALTER TABLE t ADD COLUMN a INT, ADD COLUMN b INT", true, "ALTER TABLE t", 2, "ADD COLUMN a INT"},
		{"反引号表名", "ALTER TABLE `my,t` ADD COLUMN a INT, ADD COLUMN b INT", true, "ALTER TABLE `my,t`", 2, "ADD COLUMN a INT"},
		{"PG ONLY", "alter table only t drop index x, add column a int", true, "alter table only t", 2, "drop index x"},
		{"IF EXISTS", "ALTER TABLE IF EXISTS t DROP INDEX x, ADD COLUMN a INT", true, "ALTER TABLE IF EXISTS t", 2, "DROP INDEX x"},
		{"非 ALTER", "CREATE TABLE t (id INT)", false, "", 0, ""},
		{"引号未闭合", "ALTER TABLE `t ADD COLUMN a INT", false, "", 0, ""},
		{"缺表名", "ALTER TABLE", false, "", 0, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			pfx, clauses, ok := splitAlterTable(c.in)
			if ok != c.wantOK {
				t.Fatalf("ok = %v, want %v", ok, c.wantOK)
			}
			if !ok {
				return
			}
			if pfx != c.wantPfx {
				t.Errorf("prefix = %q, want %q", pfx, c.wantPfx)
			}
			if len(clauses) != c.wantN {
				t.Fatalf("clauses = %#v, want %d 条", clauses, c.wantN)
			}
			if clauses[0] != c.wantFirst {
				t.Errorf("clauses[0] = %q, want %q", clauses[0], c.wantFirst)
			}
		})
	}
}

// ---- #98：LockKey 组合口（多副本 DDL 串行化） ----

// TestRunLockKeyEmptyNoLock 空 LockKey = 缺省行为不变：不加锁、脚本照常执行。
func TestRunLockKeyEmptyNoLock(t *testing.T) {
	db := openMigrateTestDB(t)
	opts := Options{
		Target:  "mysql",
		Install: []string{"01-install.sql"},
		FS:      testFS(),
		Root:    "ddl",
	}
	if err := Run(db, opts); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
}

// TestRunLockKeyUnsupportedDialectFailClosed 方言不支持会话级咨询锁
// （sqlite）时 LockKey 必须显式报错——不静默降级为无锁执行（调用方要的
// 是互斥，静默无锁比报错危险得多；与 RunWithAdvisoryLock 方言策略对齐）。
func TestRunLockKeyUnsupportedDialectFailClosed(t *testing.T) {
	db := openMigrateTestDB(t)
	opts := Options{
		Target:  "mysql",
		Install: []string{"01-install.sql"},
		FS:      testFS(),
		Root:    "ddl",
		LockKey: "mydb+ddl-manifest",
	}
	err := Run(db, opts)
	if !errors.Is(err, gormx.ErrAdvisoryLockUnsupported) {
		t.Fatalf("sqlite + LockKey 应报 ErrAdvisoryLockUnsupported, got: %v", err)
	}
	// fail-closed：拿锁失败（未执行）时不得执行任何脚本
	var rows int
	if qerr := db.Raw("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='t_a'").Scan(&rows).Error; qerr != nil {
		t.Fatalf("probe sqlite_master: %v", qerr)
	}
	if rows != 0 {
		t.Error("锁未拿到时不应执行任何 DDL（fail-closed 被击穿）")
	}
}

// TestLockKeyIntDeterministic 键折叠的确定性契约：同一字符串跨副本必须
// 得到同一 int64（互斥成立的前提）；不同字符串应得到不同键。
func TestLockKeyIntDeterministic(t *testing.T) {
	first, second := lockKeyInt("mydb+ddl"), lockKeyInt("mydb+ddl")
	if first != second {
		t.Fatal("同一锁键折叠结果必须一致（跨副本互斥的前提）")
	}
	other := lockKeyInt("otherdb+ddl")
	if first == other {
		t.Fatal("不同锁键折叠到同一 int64（碰撞，口径失效）")
	}
}
