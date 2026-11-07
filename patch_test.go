package gormx

import (
	"context"
	"strings"
	"testing"

	"gorm.io/gorm"
)

// patchCfg 部分更新形态模型：params 列带 CHECK 约束（非空 JSON 文档）——
// 全列 Save 把它清成 ” 会撞约束（fuxi config 事故的 sqlite 复刻形态）。
type patchCfg struct {
	BaseModel
	Name     string
	Status   int
	JobRef   string
	Params   string `gorm:"column:params;not null"`
	Assigned string
}

// patchCfgPatch 指针字段补丁：nil = 不触碰。
type patchCfgPatch struct {
	Name     *string
	Status   *int
	JobRef   *string
	Params   *string
	Assigned *string
}

func seedPatchCfg(t *testing.T, db *gorm.DB) *patchCfg {
	t.Helper()
	cfg := &patchCfg{Name: "build-go", Status: 3, JobRef: "old-ref", Params: `{"env":"dev"}`}
	if err := db.Create(cfg).Error; err != nil {
		t.Fatal(err)
	}
	return cfg
}

func strp(s string) *string { return &s }
func intp(i int) *int       { return &i }

// TestSavePatch_OnlyTouchesProvidedColumns 核心回归（#105）：nil 字段不进
// UPDATE——只传 job_ref 时 name/status/params 原值保留（Save 全列写会把
// params 清成 ” 撞约束、name 清成空串丢数据）。
func TestSavePatch_OnlyTouchesProvidedColumns(t *testing.T) {
	db := newTestDB(t, &patchCfg{})
	cfg := seedPatchCfg(t, db)

	err := SavePatch[patchCfg](context.Background(), db, cfg.ID,
		&patchCfgPatch{JobRef: strp("new-ref")})
	if err != nil {
		t.Fatalf("SavePatch: %v", err)
	}

	var got patchCfg
	if err := db.First(&got, cfg.ID).Error; err != nil {
		t.Fatal(err)
	}
	if got.JobRef != "new-ref" {
		t.Errorf("JobRef = %q, want new-ref", got.JobRef)
	}
	if got.Name != "build-go" || got.Status != 3 || got.Params != `{"env":"dev"}` {
		t.Errorf("未提供的列被改写: name=%q status=%d params=%q（Save 全列写的事故形态）",
			got.Name, got.Status, got.Params)
	}
}

// TestSavePatch_ExplicitZero 显式零值必须可写（Updates(struct) 做不到的
// 形态：status 归零是合法语义）。
func TestSavePatch_ExplicitZero(t *testing.T) {
	db := newTestDB(t, &patchCfg{})
	cfg := seedPatchCfg(t, db)

	if err := SavePatch[patchCfg](context.Background(), db, cfg.ID,
		&patchCfgPatch{Status: intp(0)}); err != nil {
		t.Fatalf("SavePatch: %v", err)
	}
	var got patchCfg
	db.First(&got, cfg.ID)
	if got.Status != 0 {
		t.Errorf("Status = %d, want 0（显式零值被吞）", got.Status)
	}
	if got.Name != "build-go" {
		t.Errorf("Name 被误改: %q", got.Name)
	}
}

// TestSavePatch_MultipleColumns 多列混合：提供 2 列、跳过 3 列。
func TestSavePatch_MultipleColumns(t *testing.T) {
	db := newTestDB(t, &patchCfg{})
	cfg := seedPatchCfg(t, db)

	if err := SavePatch[patchCfg](context.Background(), db, cfg.ID,
		&patchCfgPatch{Name: strp("renamed"), Params: strp(`{"env":"prod"}`)}); err != nil {
		t.Fatalf("SavePatch: %v", err)
	}
	var got patchCfg
	db.First(&got, cfg.ID)
	if got.Name != "renamed" || got.Params != `{"env":"prod"}` {
		t.Errorf("provided columns not written: %+v", got)
	}
	if got.Status != 3 || got.JobRef != "old-ref" {
		t.Errorf("skipped columns touched: %+v", got)
	}
}

// TestSavePatch_AllNilRejected 全 nil 补丁 = 无语句可执行：显式报错
// （而不是静默 no-op 掩盖调用方 bug）。
func TestSavePatch_AllNilRejected(t *testing.T) {
	db := newTestDB(t, &patchCfg{})
	cfg := seedPatchCfg(t, db)

	err := SavePatch[patchCfg](context.Background(), db, cfg.ID, &patchCfgPatch{})
	if err == nil || !strings.Contains(err.Error(), "未提供任何非 nil 字段") {
		t.Fatalf("全 nil 补丁应显式报错, got %v", err)
	}
	if err := SavePatch[patchCfg](context.Background(), db, cfg.ID, nil); err == nil {
		t.Fatal("nil 补丁应报错")
	}
}

// TestSavePatch_ShapeErrors 形状错误 fail-loud：非指针字段、未知字段、
// 非结构体补丁——写入前暴露，不是静默丢列。
func TestSavePatch_ShapeErrors(t *testing.T) {
	db := newTestDB(t, &patchCfg{})
	cfg := seedPatchCfg(t, db)
	ctx := context.Background()

	type nonPtrPatch struct {
		Name string // 非指针：表达不了「未提供」
	}
	err := SavePatch[patchCfg](ctx, db, cfg.ID, &nonPtrPatch{Name: "x"})
	if err == nil || !strings.Contains(err.Error(), "必须为指针类型") {
		t.Errorf("非指针字段应报错, got %v", err)
	}

	type unknownPatch struct {
		Bogus *string // 模拟拼写错误的字段名
	}
	err = SavePatch[patchCfg](ctx, db, cfg.ID, &unknownPatch{Bogus: strp("x")})
	if err == nil || !strings.Contains(err.Error(), "无同名字段") {
		t.Errorf("未知字段应报错（拼写错误写入前暴露）, got %v", err)
	}

	err = SavePatch[patchCfg](ctx, db, cfg.ID, "not-a-struct")
	if err == nil || !strings.Contains(err.Error(), "struct") {
		t.Errorf("非结构体补丁应报错, got %v", err)
	}
}

// TestSavePatch_UnmatchedPKNotError 未命中的 pk：语句成功但 0 行——
// 不从 RowsAffected 推断 not-found（驱动按「值实际变化」计数的口径），
// 调用方需要 not-found 语义时先读后判（文档契约的行为锚定）。
func TestSavePatch_UnmatchedPKNotError(t *testing.T) {
	db := newTestDB(t, &patchCfg{})
	seedPatchCfg(t, db)

	err := SavePatch[patchCfg](context.Background(), db, int64(999),
		&patchCfgPatch{Name: strp("ghost")})
	if err != nil {
		t.Fatalf("未命中 pk 不应报错（RowsAffected 口径不可靠）, got %v", err)
	}
	var n int64
	db.Model(&patchCfg{}).Where("name = ?", "ghost").Count(&n)
	if n != 0 {
		t.Errorf("未命中 pk 却写入了 %d 行", n)
	}
}

// TestSavePatch_UpdatedAtMaintained gorm Updates 语义下 UpdatedAt 自动维护。
func TestSavePatch_UpdatedAtMaintained(t *testing.T) {
	db := newTestDB(t, &patchCfg{})
	cfg := seedPatchCfg(t, db)
	before := cfg.UpdatedAt

	if err := SavePatch[patchCfg](context.Background(), db, cfg.ID,
		&patchCfgPatch{Name: strp("touched")}); err != nil {
		t.Fatal(err)
	}
	var got patchCfg
	db.First(&got, cfg.ID)
	if !got.UpdatedAt.After(before) {
		t.Errorf("UpdatedAt 未维护: before=%v after=%v", before, got.UpdatedAt)
	}
}

// TestSavePatch_ColumnNameResolution 列名解析经 schema（字段名 ≠ 列名形态）。
func TestSavePatch_ColumnNameResolution(t *testing.T) {
	type snakeCfg struct {
		BaseModel
		JobRef string `gorm:"column:job_ref"`
	}
	type snakePatch struct {
		JobRef *string
	}
	db := newTestDB(t, &snakeCfg{})
	in := &snakeCfg{JobRef: "a"}
	if err := db.Create(in).Error; err != nil {
		t.Fatal(err)
	}
	if err := SavePatch[snakeCfg](context.Background(), db, in.ID, &snakePatch{JobRef: strp("b")}); err != nil {
		t.Fatalf("SavePatch: %v", err)
	}
	var got snakeCfg
	db.First(&got, in.ID)
	if got.JobRef != "b" {
		t.Errorf("JobRef = %q, want b（列名解析失败）", got.JobRef)
	}
}
