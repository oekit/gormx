package gormx

// sequences_test.go —— 序列对齐（#226）。活体 PG 集成形态与 argus 相同：
// 无 DSN 环境变量即跳过（CI 不起 PG 容器）；方言门控/引号转义纯函数本地全测。

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

// TestRepairSequences_UnsupportedDialect 非 PG 方言显式报错（advisory_lock
// 同口径：维护诉求在不支持方言上必须报错而不是静默跳过）。
func TestRepairSequences_UnsupportedDialect(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = RepairSequences(context.Background(), db)
	if !errors.Is(err, ErrSequenceRepairUnsupported) {
		t.Fatalf("sqlite 应返回 ErrSequenceRepairUnsupported, got %v", err)
	}

	_, err = RepairSequences(context.Background(), nil)
	if err == nil || err == ErrSequenceRepairUnsupported {
		t.Fatalf("nil db 应返回参数错误, got %v", err)
	}
}

// TestQuoteIdent 引号转义：内嵌双引号按 PG 规则翻倍；常规标识符原样钉住。
func TestQuoteIdent(t *testing.T) {
	cases := map[string]string{
		"finding_prints_id_seq": `"finding_prints_id_seq"`,
		`we"ird_seq`:            `"we""ird_seq"`,
		"a":                     `"a"`,
	}
	for in, want := range cases {
		if got := quoteIdent(in); got != want {
			t.Fatalf("quoteIdent(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestRepairSequences_LivePG 活体 PG（GORMX_TEST_PG_DSN）：复刻生产事故形态——
// 显式 ID 灌入（不经序列）→ 修复 → nextval 越过 max 且新插入成功 → 二次运行零修复。
func TestRepairSequences_LivePG(t *testing.T) {
	dsn := os.Getenv("GORMX_TEST_PG_DSN")
	if dsn == "" {
		t.Skip("GORMX_TEST_PG_DSN 未设置（PG 集成专用）")
	}
	openPG := func(t *testing.T) *gorm.DB {
		t.Helper()
		db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
		if err != nil {
			t.Fatalf("open pg: %v", err)
		}
		return db
	}
	ctx := context.Background()
	db := openPG(t)

	// 事故复刻：显式 ID 灌入 + 序列停在 1。
	mustExec(t, db, `DROP TABLE IF EXISTS seq_repair_probe`)
	mustExec(t, db, `CREATE TABLE seq_repair_probe (id bigserial PRIMARY KEY, note text)`)
	mustExec(t, db, `INSERT INTO seq_repair_probe (id, note) VALUES (101, 'a'), (102, 'b')`)
	mustExec(t, db, `SELECT setval('seq_repair_probe_id_seq', 1, false)`)
	t.Cleanup(func() { mustExec(t, db, `DROP TABLE IF EXISTS seq_repair_probe`) })

	repaired, err := RepairSequences(ctx, db)
	if err != nil {
		t.Fatalf("RepairSequences: %v", err)
	}
	found := false
	for _, tb := range repaired {
		if tb == "seq_repair_probe" {
			found = true
		}
	}
	if !found {
		t.Fatalf("seq_repair_probe 应被修复（显式 ID 灌入后序列落后）: %v", repaired)
	}

	// 修复后走自增的插入必须成功且 id > 102。
	var id int64
	if err := db.WithContext(ctx).Raw(
		`INSERT INTO seq_repair_probe (note) VALUES ('c') RETURNING id`).Scan(&id).Error; err != nil {
		t.Fatalf("修复后插入失败（序列仍落后）: %v", err)
	}
	if id <= 102 {
		t.Fatalf("修复后新 id=%d 应 > 102", id)
	}

	// 幂等：二次运行零修复。
	again, err := RepairSequences(ctx, db)
	if err != nil {
		t.Fatalf("二次 RepairSequences: %v", err)
	}
	for _, tb := range again {
		if tb == "seq_repair_probe" {
			t.Fatalf("二次运行不应再修复 seq_repair_probe: %v", again)
		}
	}
}

func mustExec(t *testing.T, db *gorm.DB, sql string) {
	t.Helper()
	if err := db.Exec(sql).Error; err != nil {
		t.Fatalf("exec %q: %v", sql, err)
	}
}
