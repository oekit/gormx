package gormx

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"strings"
	"testing"
	"time"

	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gclause "gorm.io/gorm/clause"
)

// offlineConnector 是永不连线的 driver.Connector：DryRun 只产 SQL 不执行，
// Connect 永不被调；一旦被调即显式报错，防止测试悄悄走到真实网络。
type offlineConnector struct{}

func (offlineConnector) Connect(context.Context) (driver.Conn, error) {
	return nil, errors.New("offline: DryRun only, Connect must not be called")
}
func (offlineConnector) Driver() driver.Driver { return nil }

func newMySQLDryRunDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(mysql.New(mysql.Config{
		Conn:                      sql.OpenDB(offlineConnector{}),
		SkipInitializeWithVersion: true,
	}), &gorm.Config{DryRun: true, DisableAutomaticPing: true})
	if err != nil {
		t.Fatalf("open mysql dryrun: %v", err)
	}
	return db
}

func newPostgresDryRunDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(postgres.New(postgres.Config{
		Conn: sql.OpenDB(offlineConnector{}),
	}), &gorm.Config{DryRun: true, DisableAutomaticPing: true})
	if err != nil {
		t.Fatalf("open postgres dryrun: %v", err)
	}
	return db
}

// stubDialector 仅用于「未知方言」panic 断言：内嵌 nil Dialector 兜全接口，
// 但只覆写 Name/QuoteTo——测试仅经 &gorm.DB{Config: ...} 直接构造，其余
// 方法永不被调（gorm.Open 会调 Initialize，nil 内嵌方法即崩，不走 Open）。
type stubDialector struct {
	gorm.Dialector
	name string
}

func (s stubDialector) Name() string { return s.name }
func (s stubDialector) QuoteTo(w gclause.Writer, str string) {
	_, _ = w.WriteString(str)
}

// bucketRow 是 DryRun/e2e 共用的最小时间列模型（表名 bucket_rows）。
type bucketRow struct {
	ID        int64
	CreatedAt time.Time
}

func TestQuoteIdentifier_MySQL(t *testing.T) {
	db := newMySQLDryRunDB(t)
	cases := []struct{ in, want string }{
		{"user", "`user`"},                 // 保留字
		{"order", "`order`"},               // 保留字
		{"schema.user", "`schema`.`user`"}, // 限定名分段
		{"a`b", "`a``b`"},                  // 内嵌引号转义
		{"`already`", "`already`"},         // 已引用原样保留
		{"schema.`already`", "`schema`.`already`"},
	}
	for _, c := range cases {
		if got := QuoteIdentifier(db, c.in); got != c.want {
			t.Errorf("mysql QuoteIdentifier(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	if got := QuoteIdentifier(db, ""); got != "" {
		t.Errorf("empty name must be no-op, got %q", got)
	}
}

func TestQuoteIdentifier_Postgres(t *testing.T) {
	db := newPostgresDryRunDB(t)
	cases := []struct{ in, want string }{
		{"user", `"user"`},
		{"order", `"order"`},
		{"schema.user", `"schema"."user"`},
		{`a"b`, `"a""b"`},
		{`"already"`, `"already"`},
	}
	for _, c := range cases {
		if got := QuoteIdentifier(db, c.in); got != c.want {
			t.Errorf("postgres QuoteIdentifier(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestQuoteIdentifier_SQLite(t *testing.T) {
	db := newTestDB(t)
	// glebarez sqlite 的 QuoteTo 走 MySQL 风格反引号（SQLite 亦接受）
	cases := []struct{ in, want string }{
		{"user", "`user`"},
		{"schema.user", "`schema`.`user`"},
		{"a`b", "`a``b`"},
	}
	for _, c := range cases {
		if got := QuoteIdentifier(db, c.in); got != c.want {
			t.Errorf("sqlite QuoteIdentifier(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestQuoteIdentifier_NilDBPanics(t *testing.T) {
	assertPanic(t, "nil db", func() { QuoteIdentifier(nil, "user") })
	assertPanic(t, "nil dialector", func() {
		QuoteIdentifier(&gorm.DB{}, "user")
	})
}

func TestQuoteIdentifier_UsedInOrderClause(t *testing.T) {
	// 验收面（#189）：动态排序列经引用后拼进语句，两方言各自获得正确的引用形态
	db := newMySQLDryRunDB(t)
	stmt := db.Session(&gorm.Session{DryRun: true}).Model(&bucketRow{}).
		Order(QuoteIdentifier(db, "order") + " DESC").
		Find(&[]bucketRow{}).Statement
	if !strings.Contains(stmt.SQL.String(), "ORDER BY `order` DESC") {
		t.Fatalf("mysql dryrun ORDER BY = %s, want quoted reserved word", stmt.SQL.String())
	}
}

func TestNowMinusDaysExpr_DialectSQL(t *testing.T) {
	cases := []struct {
		name     string
		build    func() *gorm.DB
		delta    func(db *gorm.DB, days int) gclause.Expr
		wantSQL  string
		wantVars int
	}{
		{"mysql now", func() *gorm.DB { return newMySQLDryRunDB(t) }, NowMinusDays,
			"DATE_SUB(NOW(), INTERVAL ? DAY)", 1},
		{"postgres now", func() *gorm.DB { return newPostgresDryRunDB(t) }, NowMinusDays,
			"now() - make_interval(days => $1::int)", 1},
		{"mysql today", func() *gorm.DB { return newMySQLDryRunDB(t) }, TodayMinusDays,
			"DATE_SUB(CURRENT_DATE(), INTERVAL ? DAY)", 1},
		{"postgres today", func() *gorm.DB { return newPostgresDryRunDB(t) }, TodayMinusDays,
			"CURRENT_DATE - make_interval(days => $1::int)", 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			db := c.build()
			stmt := db.Session(&gorm.Session{DryRun: true}).Model(&bucketRow{}).
				Where("created_at >= (?)", c.delta(db, 30)).
				Find(&[]bucketRow{}).Statement
			if !strings.Contains(stmt.SQL.String(), c.wantSQL) {
				t.Fatalf("SQL = %s, want it to contain %q", stmt.SQL.String(), c.wantSQL)
			}
			if len(stmt.Vars) != c.wantVars || stmt.Vars[0] != 30 {
				t.Fatalf("bind vars = %v, want [30]（业务值必须走绑定参数）", stmt.Vars)
			}
		})
	}
}

func TestTodayMinusDaysExpr_SQLiteDryRun(t *testing.T) {
	db := newTestDB(t)
	stmt := db.Session(&gorm.Session{DryRun: true}).Model(&bucketRow{}).
		Where("created_at >= (?)", TodayMinusDays(db, 7)).
		Find(&[]bucketRow{}).Statement
	if !strings.Contains(stmt.SQL.String(), "date('now', '-' || ? || ' days')") {
		t.Fatalf("sqlite today SQL = %s", stmt.SQL.String())
	}
	if len(stmt.Vars) != 1 || stmt.Vars[0] != 7 {
		t.Fatalf("bind vars = %v, want [7]", stmt.Vars)
	}
}

func TestDateBucketExpr_DialectSQL(t *testing.T) {
	cases := []struct {
		name    string
		build   func() *gorm.DB
		wantSQL string
	}{
		{"mysql", func() *gorm.DB { return newMySQLDryRunDB(t) }, "DATE(`created_at`)"},
		{"postgres", func() *gorm.DB { return newPostgresDryRunDB(t) }, `to_char("created_at", 'YYYY-MM-DD')`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			db := c.build()
			stmt := db.Session(&gorm.Session{DryRun: true}).Model(&bucketRow{}).
				Select(DateBucket(db, "created_at").SQL + " AS bucket").
				Find(&[]bucketRow{}).Statement
			if !strings.Contains(stmt.SQL.String(), c.wantSQL) {
				t.Fatalf("SQL = %s, want it to contain %q", stmt.SQL.String(), c.wantSQL)
			}
		})
	}
}

func TestDateExprs_UnknownDialectPanics(t *testing.T) {
	// 未知方言在产出 SQL 前构造期 panic（不做静默回退——静默产出别家方言的
	// SQL 是运行期语法错误远离根因的形态）。
	bogus := &gorm.DB{Config: &gorm.Config{Dialector: stubDialector{name: "bogus"}}}
	assertPanic(t, "NowMinusDays", func() { NowMinusDays(bogus, 30) })
	assertPanic(t, "TodayMinusDays", func() { TodayMinusDays(bogus, 30) })
	assertPanic(t, "DateBucket", func() { DateBucket(bogus, "col") })
	assertPanic(t, "DateBucket empty column", func() {
		DateBucket(newMySQLDryRunDB(t), "")
	})
}

// TestDateExprs_SQLiteEndToEnd 日期边界（#189 验收面）：真实 sqlite 上
// 「现在减 N 天」「今天减 N 天」「文本日期分桶」的过滤/聚合结果正确，
// 业务值走绑定参数。
func TestDateExprs_SQLiteEndToEnd(t *testing.T) {
	db := newTestDB(t)
	if err := db.Exec(`CREATE TABLE bucket_rows (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		created_at DATETIME NOT NULL
	)`).Error; err != nil {
		t.Fatalf("create table: %v", err)
	}
	now := time.Now().UTC()
	layout := "2006-01-02 15:04:05"
	rows := []string{
		now.Format(layout),                    // 今天
		now.AddDate(0, 0, -10).Format(layout), // 近 30 天内
		now.AddDate(0, 0, -40).Format(layout), // 30 天外
	}
	for _, ts := range rows {
		if err := db.Exec("INSERT INTO bucket_rows (created_at) VALUES (?)", ts).Error; err != nil {
			t.Fatalf("seed %s: %v", ts, err)
		}
	}
	ctx := context.Background()

	// 现在减 30 天（滚动窗口）：30 天内的两行被命中，40 天前的被排除
	var recent int64
	if err := db.WithContext(ctx).Model(&bucketRow{}).
		Where("created_at >= (?)", NowMinusDays(db, 30)).
		Count(&recent).Error; err != nil {
		t.Fatalf("NowMinusDays count: %v", err)
	}
	if recent != 2 {
		t.Fatalf("NowMinusDays(30) matched %d rows, want 2", recent)
	}

	// 今天减 30 天（自然日窗口）：边界日当天 00:00 起算，命中两行
	var byDay int64
	if err := db.WithContext(ctx).Model(&bucketRow{}).
		Where("created_at >= (?)", TodayMinusDays(db, 30)).
		Count(&byDay).Error; err != nil {
		t.Fatalf("TodayMinusDays count: %v", err)
	}
	if byDay != 2 {
		t.Fatalf("TodayMinusDays(30) matched %d rows, want 2", byDay)
	}

	// 文本日期分桶：按天聚合，今天与 -10 天各一桶、各 1 条
	var buckets []struct {
		Bucket string
		N      int64
	}
	e := DateBucket(db, "created_at")
	if err := db.WithContext(ctx).Model(&bucketRow{}).
		Select(e.SQL + " AS bucket, COUNT(*) AS n").
		Group("bucket").
		Order("bucket DESC").
		Scan(&buckets).Error; err != nil {
		t.Fatalf("DateBucket group: %v", err)
	}
	if len(buckets) != 3 {
		t.Fatalf("DateBucket got %d buckets (%v), want 3", len(buckets), buckets)
	}
	for _, b := range buckets {
		if b.N != 1 {
			t.Fatalf("bucket %s has %d rows, want 1", b.Bucket, b.N)
		}
		if len(b.Bucket) != len("YYYY-MM-DD") || b.Bucket[4] != '-' || b.Bucket[7] != '-' {
			t.Fatalf("bucket %q is not YYYY-MM-DD text", b.Bucket)
		}
	}
}

func assertPanic(t *testing.T, what string, fn func()) {
	t.Helper()
	defer func() {
		if r := recover(); r == nil {
			t.Errorf("%s must panic (fail-fast), got nil", what)
		}
	}()
	fn()
}
