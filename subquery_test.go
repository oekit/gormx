package gormx

import (
	"strings"
	"testing"

	gmysql "gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// sweepLog 保留期清扫形态的模型。
type sweepLog struct {
	BaseModel
	State string
}

// outboxMsg 批量 CAS 认领形态的模型。
type outboxMsg struct {
	BaseModel
	Status    int
	ClaimedBy string
}

// ---- 纯函数面：方言分发 ----

func TestWrapsDerivedTable_DialectDispatch(t *testing.T) {
	if !wrapsDerivedTable("mysql") {
		t.Error("mysql 应派生表包装（error 1093 + IN 子查询内 LIMIT 限制）")
	}
	for _, d := range []string{"sqlite", "postgres"} {
		if wrapsDerivedTable(d) {
			t.Errorf("%s 无 1093 限制，应直出", d)
		}
	}
}

// ---- DryRun 面：MySQL 派生表形态（无服务器，SkipInitializeWithVersion 不连库） ----

// openDryRunMySQL 构造不落连接的 mysql dialector（SkipInitializeWithVersion
// 跳过版本查询 + DisableAutomaticPing 跳过拨号；sql.Open 惰性注册，DryRun
// 语句构建全程不触网），用于断言派生表包装的 SQL 形态。
func openDryRunMySQL(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(gmysql.New(gmysql.Config{
		DSN:                       "gormx_dry_run:gormx@tcp(127.0.0.1:1)/gormx_dry_run",
		SkipInitializeWithVersion: true,
	}), &gorm.Config{
		Logger:               logger.Default.LogMode(logger.Silent),
		DryRun:               true,
		DisableAutomaticPing: true,
	})
	if err != nil {
		t.Fatalf("open dry-run mysql: %v", err)
	}
	t.Cleanup(func() {
		sqlDB, _ := db.DB()
		if sqlDB != nil {
			_ = sqlDB.Close()
		}
	})
	return db
}

func TestSubqueryIn_MySQLDerivedTableShape(t *testing.T) {
	db := openDryRunMySQL(t)
	sub := db.Model(&sweepLog{}).Select("id").Where("state = ?", "finished").Limit(3)
	var dest []sweepLog
	tx := SubqueryIn(db.Model(&sweepLog{}), "id", sub).Find(&dest)

	sql := tx.Statement.SQL.String()
	// 派生表包装形态：id IN (SELECT id FROM (SELECT ... ) as gormx_dt)
	if !strings.Contains(sql, "IN (SELECT id FROM (") {
		t.Errorf("mysql 应派生表包装, got: %s", sql)
	}
	if !strings.Contains(sql, "as gormx_dt") {
		t.Errorf("派生表应命名, got: %s", sql)
	}
	if !strings.Contains(sql, "LIMIT ?") {
		t.Errorf("IN 子查询内 LIMIT 应保留（派生表内合法）, got: %s", sql)
	}
	// 子查询参数原序绑定（含 LIMIT 参数）
	if len(tx.Statement.Vars) != 2 || tx.Statement.Vars[0] != "finished" || tx.Statement.Vars[1] != 3 {
		t.Errorf("sub query vars = %v, want [finished 3]", tx.Statement.Vars)
	}
}

// ---- sqlite 集成面：真实执行 ----

func TestDeleteBySubquery_SQLite(t *testing.T) {
	db := newTestDB(t, &sweepLog{})
	for i := 0; i < 10; i++ {
		state := "finished"
		if i >= 7 {
			state = "running"
		}
		if err := db.Create(&sweepLog{State: state}).Error; err != nil {
			t.Fatal(err)
		}
	}
	sub := db.Model(&sweepLog{}).Select("id").Where("state = ?", "finished")
	res := DeleteBySubquery(db, &sweepLog{}, "id", sub)
	if res.Error != nil {
		t.Fatalf("delete: %v", res.Error)
	}
	if res.RowsAffected != 7 {
		t.Errorf("RowsAffected = %d, want 7", res.RowsAffected)
	}
	var left int64
	db.Model(&sweepLog{}).Count(&left)
	if left != 3 {
		t.Errorf("剩余 %d 行, want 3", left)
	}
}

// TestDeleteBySubquery_SQLiteWithLimit 批量形态：LIMIT 子查询按批删除
// （保留期清扫的常规写法——每批 N 条）。
func TestDeleteBySubquery_SQLiteWithLimit(t *testing.T) {
	db := newTestDB(t, &sweepLog{})
	for i := 0; i < 10; i++ {
		if err := db.Create(&sweepLog{State: "finished"}).Error; err != nil {
			t.Fatal(err)
		}
	}
	sub := db.Model(&sweepLog{}).Select("id").Where("state = ?", "finished").Limit(4)
	res := DeleteBySubquery(db, &sweepLog{}, "id", sub)
	if res.Error != nil {
		t.Fatalf("delete: %v", res.Error)
	}
	if res.RowsAffected != 4 {
		t.Errorf("RowsAffected = %d, want 4（LIMIT 生效）", res.RowsAffected)
	}
	var left int64
	db.Model(&sweepLog{}).Count(&left)
	if left != 6 {
		t.Errorf("剩余 %d 行, want 6", left)
	}
}

// TestSubqueryIn_BatchCASClaim 批量 CAS 认领：一条语句完成多节点互斥认领
// （替代逐行 UPDATE 循环的 N 次 round-trip；外层 guard + IN 子查询同语句）。
func TestSubqueryIn_BatchCASClaim(t *testing.T) {
	db := newTestDB(t, &outboxMsg{})
	for i := 0; i < 20; i++ {
		if err := db.Create(&outboxMsg{Status: 0}).Error; err != nil {
			t.Fatal(err)
		}
	}
	pending := func() *gorm.DB {
		return db.Model(&outboxMsg{}).Where("status = ?", 0).Select("id").Order("id").Limit(5)
	}
	res := SubqueryIn(db.Model(&outboxMsg{}).Where("status = ?", 0), "id", pending()).
		Updates(map[string]any{"status": 1, "claimed_by": "node-a"})
	if res.Error != nil {
		t.Fatalf("claim: %v", res.Error)
	}
	if res.RowsAffected != 5 {
		t.Fatalf("RowsAffected = %d, want 5", res.RowsAffected)
	}

	var claimed []outboxMsg
	if err := db.Where("claimed_by = ?", "node-a").Order("id").Find(&claimed).Error; err != nil {
		t.Fatal(err)
	}
	if len(claimed) != 5 {
		t.Fatalf("claimed %d 条, want 5", len(claimed))
	}
	for i, m := range claimed {
		if m.ID != int64(i+1) || m.Status != 1 {
			t.Errorf("claimed[%d] = {id:%d status:%d}, want {id:%d status:1}", i, m.ID, m.Status, i+1)
		}
	}

	// 第二个节点再认领：拿到下一批 5 条，不与 node-a 重叠
	res = SubqueryIn(db.Model(&outboxMsg{}).Where("status = ?", 0), "id", pending()).
		Updates(map[string]any{"status": 1, "claimed_by": "node-b"})
	if res.Error != nil || res.RowsAffected != 5 {
		t.Fatalf("second claim: rows=%d err=%v, want 5/nil", res.RowsAffected, res.Error)
	}
	var overlap int64
	db.Model(&outboxMsg{}).Where("claimed_by = ? AND status = ?", "node-a", 0).Count(&overlap)
	if overlap != 0 {
		t.Errorf("node-a 的认领被 node-b 覆盖 %d 条", overlap)
	}
}

// TestSubqueryIn_ArgumentErrors 入参错误经 AddError 走 gorm 错误面（不 panic）。
func TestSubqueryIn_ArgumentErrors(t *testing.T) {
	db := newTestDB(t, &sweepLog{})

	tx := SubqueryIn(db.Model(&sweepLog{}), "", db.Model(&sweepLog{}).Select("id"))
	if tx.Error == nil || !strings.Contains(tx.Error.Error(), "column") {
		t.Errorf("空 column 应报错, got %v", tx.Error)
	}
	tx = SubqueryIn(db.Model(&sweepLog{}), "id", nil)
	if tx.Error == nil || !strings.Contains(tx.Error.Error(), "subquery") {
		t.Errorf("nil subquery 应报错, got %v", tx.Error)
	}
}

// TestSubqueryIn_ColumnIdentifierWhitelist #122 回归：column 须过标识符
// 白名单（#100 同款纪律）fail-closed——mysql 派生表路径经 Select(string)
// 原样渲染，白名单外的值（逗号多列/括号表达式/子查询）此前会被原样拼进
// `SELECT <column> FROM (...) AS gormx_dt` 的裸拼面。issue 探针原样：
// column="id, (SELECT password FROM users LIMIT 1)" 必须报错且 SQL 零泄漏。
func TestSubqueryIn_ColumnIdentifierWhitelist(t *testing.T) {
	db := openDryRunMySQL(t)
	injections := []string{
		"id, (SELECT password FROM users LIMIT 1)", // issue 探针：表达式/子查询
		"id); DROP TABLE users; --",                // 括号闭合 + 语句拼接
		"id OR 1=1",                                // 空格表达式
		"id ASC, password",                         // 多列
		"`id`",                                     // 反引号自引用
		`"id"`,                                     // 双引号包裹
	}
	for _, col := range injections {
		sub := db.Model(&sweepLog{}).Select("id").Where("state = ?", "finished")
		tx := SubqueryIn(db.Model(&sweepLog{}), col, sub)
		if tx.Error == nil {
			t.Errorf("column %q 应被白名单拒绝（fail-closed）", col)
			continue
		}
		if !strings.Contains(tx.Error.Error(), "identifier") {
			t.Errorf("column %q 的报错应点明白名单: %v", col, tx.Error)
		}
		if sql := tx.Statement.SQL.String(); strings.Contains(sql, "password") || strings.Contains(sql, "DROP") {
			t.Errorf("column %q 的注入片段泄漏进 SQL: %s", col, sql)
		}
	}

	// 白名单内形态照常工作：裸列名、表限定名不拒（与 crud Where/Or 同宽）。
	for _, col := range []string{"id", "t.id", "_col2"} {
		sub := db.Model(&sweepLog{}).Select("id").Where("state = ?", "finished")
		tx := SubqueryIn(db.Model(&sweepLog{}), col, sub)
		if tx.Error != nil {
			t.Errorf("column %q 应过白名单, got %v", col, tx.Error)
		}
	}
}
