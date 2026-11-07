package gormx

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/google/uuid"
	"gorm.io/gorm/logger"
)

// ---- 单元面：redactor ----

func TestRedactor_Assignments(t *testing.T) {
	r := newRedactor([]string{"password", "token"})
	cases := []struct{ name, in, want string }{
		{
			"UPDATE SET 引号列",
			"UPDATE `config` SET `name`='x', `password`='secret' WHERE id = 9",
			"UPDATE `config` SET `name`='x', `password`=*** WHERE id = 9",
		},
		{
			"UPDATE SET 裸列",
			"UPDATE config SET password = 'secret123' WHERE id = 9",
			"UPDATE config SET password = *** WHERE id = 9",
		},
		{
			"WHERE 比较",
			"SELECT * FROM users WHERE token = 'tok-abc' AND name = 'alice'",
			"SELECT * FROM users WHERE token = *** AND name = 'alice'",
		},
		{
			"裸值（数字）",
			"UPDATE t SET token = 42 WHERE id = 1",
			"UPDATE t SET token = *** WHERE id = 1",
		},
		{
			"非敏感列不动",
			"UPDATE t SET name = 'alice', age = 3 WHERE id = 1",
			"UPDATE t SET name = 'alice', age = 3 WHERE id = 1",
		},
	}
	for _, c := range cases {
		if got := r.redact(c.in); got != c.want {
			t.Errorf("%s:\n got %s\nwant %s", c.name, got, c.want)
		}
	}
}

func TestRedactor_InsertTuples(t *testing.T) {
	r := newRedactor([]string{"password", "request_body"})
	cases := []struct{ name, in, want string }{
		{
			"单行元组",
			"INSERT INTO `users` (`name`,`password`) VALUES ('alice','secret')",
			"INSERT INTO `users` (`name`,`password`) VALUES ('alice',***)",
		},
		{
			"多行元组",
			"INSERT INTO `users` (`name`,`password`) VALUES ('a','p1'),('b','p2')",
			"INSERT INTO `users` (`name`,`password`) VALUES ('a',***), ('b',***)",
		},
		{
			"值含逗号（引号保护）",
			"INSERT INTO t (a,request_body) VALUES (1,'{\"k\":\"v,w\"}')",
			"INSERT INTO t (a,request_body) VALUES (1,***)",
		},
		{
			"无敏感列原样",
			"INSERT INTO t (a,b) VALUES (1,2)",
			"INSERT INTO t (a,b) VALUES (1,2)",
		},
		{
			"列名大小写与引号不敏感",
			"INSERT INTO t (`Request_Body`) VALUES ('x')",
			"INSERT INTO t (`Request_Body`) VALUES (***)",
		},
	}
	for _, c := range cases {
		if got := r.redact(c.in); got != c.want {
			t.Errorf("%s:\n got %s\nwant %s", c.name, got, c.want)
		}
	}
}

func TestRedactor_EmptyIsNil(t *testing.T) {
	if newRedactor(nil) != nil || newRedactor([]string{}) != nil {
		t.Error("空列集应返回 nil（不脱敏，零开销直通）")
	}
	var r *redactor
	if got := r.redact("UPDATE t SET password='x'"); got != "UPDATE t SET password='x'" {
		t.Errorf("nil redactor 应直通, got %s", got)
	}
}

// ---- Trace 集成面：脱敏在日志出口生效 ----

// TestLogger_TraceRedactsSensitiveColumns Trace 输出的 sql 字段经脱敏：
// 敏感值 ***，语句结构保留。
func TestLogger_TraceRedactsSensitiveColumns(t *testing.T) {
	buf := captureStdLog(t)
	l := NewDefaultLogger(WithRedactColumns("password"))

	l.Trace(context.Background(), time.Now(), func() (string, int64) {
		return "UPDATE `config` SET `password`='secret-value' WHERE id = 9", 1
	}, nil)

	out := buf.String()
	if !strings.Contains(out, "***") {
		t.Errorf("敏感值应脱敏, got: %q", out)
	}
	if strings.Contains(out, "secret-value") {
		t.Errorf("敏感值泄漏, got: %q", out)
	}
	if !strings.Contains(out, "`password`") || !strings.Contains(out, "WHERE id = 9") {
		t.Errorf("语句结构应保留（排障不失真）, got: %q", out)
	}
}

// TestLogger_TraceNoRedactionByDefault 未配置脱敏时行为不变（值原样——
// 向后兼容基线，脱敏是 opt-in）。
func TestLogger_TraceNoRedactionByDefault(t *testing.T) {
	buf := captureStdLog(t)
	l := NewDefaultLogger()

	l.Trace(context.Background(), time.Now(), func() (string, int64) {
		return "UPDATE `config` SET `password`='plain' WHERE id = 9", 1
	}, nil)

	if !strings.Contains(buf.String(), "plain") {
		t.Errorf("未配置脱敏应原样输出, got: %q", buf.String())
	}
}

// TestOpen_WithLoggerRedactionEndToEnd Open + WithLogger + WithRedactColumns
// 的端到端形态：真实执行一条写语句，日志出口的敏感值已脱敏。
func TestOpen_WithLoggerRedactionEndToEnd(t *testing.T) {
	buf := captureStdLog(t)
	db, err := Open(
		sqlite.Open("file:"+uuid.New().String()+"?mode=memory&cache=shared"),
		WithLogger(NewDefaultLogger(WithRedactColumns("password"))),
	)
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := db.DB()
	t.Cleanup(func() { _ = sqlDB.Close() })

	if err := db.Exec("CREATE TABLE users (id INTEGER PRIMARY KEY, name TEXT, password TEXT)").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("INSERT INTO users (name, password) VALUES ('alice', 'hunter2')").Error; err != nil {
		t.Fatal(err)
	}

	out := buf.String()
	if strings.Contains(out, "hunter2") {
		t.Errorf("INSERT 的敏感值泄漏进日志: %q", out)
	}
	if !strings.Contains(out, "***") {
		t.Errorf("脱敏占位应出现在日志: %q", out)
	}
	if !strings.Contains(out, "INSERT INTO users") {
		t.Errorf("语句结构应保留: %q", out)
	}
}

// TestLogger_LogModeKeepsRedaction LogMode 副本共享脱敏配置。
func TestLogger_LogModeKeepsRedaction(t *testing.T) {
	buf := captureStdLog(t)
	l := NewDefaultLogger(WithRedactColumns("token")).LogMode(logger.Info).(interface {
		Trace(ctx context.Context, begin time.Time, fc func() (string, int64), err error)
	})
	l.Trace(context.Background(), time.Now(), func() (string, int64) {
		return "SELECT * FROM t WHERE token = 'tk'", 0
	}, nil)
	if !strings.Contains(buf.String(), "***") || strings.Contains(buf.String(), "tk") {
		t.Errorf("LogMode 副本应保留脱敏, got: %q", buf.String())
	}
}
