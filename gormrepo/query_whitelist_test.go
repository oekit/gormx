package gormrepo

import (
	"strings"
	"testing"

	"github.com/oekit/gormx/crud"
	"gorm.io/gorm"
)

// querySQL 用 DryRun 渲染 ApplyQuery 后的 SELECT 语句文本（值不落库）。
func querySQL(t *testing.T, db *gorm.DB, q *crud.Query) string {
	t.Helper()
	tx := db.Session(&gorm.Session{DryRun: true})
	stmt := ApplyQuery(tx, q).Find(&testUser{}).Statement
	return stmt.SQL.String()
}

// TestApplyQuery_InjectionPayloadsNotInSQL 回归（#154）：Order/GroupBy/Select
// 的字符串此前原样进入 GORM 的 raw 拼接面（db.Order/db.Group/db.Select），
// 前端可控的排序/分组/投影字段透传即 SQL 注入。修复后白名单外的值在 crud
// 入口即被丢弃，SQL 文本不含任何注入载荷。
func TestApplyQuery_InjectionPayloadsNotInSQL(t *testing.T) {
	db := setupTestDB(t)
	markers := []string{"DROP TABLE", "UNION SELECT", "password FROM"}

	cases := []struct {
		name string
		q    *crud.Query
	}{
		{"Order 注入", crud.NewQuery().Order("id; DROP TABLE users; --")},
		{"Order UNION 注入", crud.NewQuery().Order("id) UNION SELECT password FROM users --")},
		{"GroupBy 注入", crud.NewQuery().GroupBy("id) UNION SELECT password FROM users --")},
		{"Select 注入", crud.NewQuery().Select("id, (SELECT password FROM users)")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sql := querySQL(t, db, tc.q)
			for _, m := range markers {
				if strings.Contains(sql, m) {
					t.Errorf("注入载荷进入了 SQL：%s", sql)
				}
			}
		})
	}
}
