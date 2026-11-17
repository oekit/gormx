package gormx

import (
	"strings"
	"testing"
)

// pkComposite 复合主键模型（ID + UID 双主键）：验证单源函数对复合主键的
// 「透传」语义——本层不报错（是否合法由调用方语义决定），返回优先化主键
// 字段，调用方经 field.Schema.PrimaryFields 自行校验（如 SavePatch）。
type pkComposite struct {
	ID   int64  `gorm:"primaryKey"`
	UID  int64  `gorm:"primaryKey"`
	Name string `gorm:"size:50"`
}

// pkNoPK 无主键列模型：单源函数须显式报错（不能静默返回空列名）。
type pkNoPK struct {
	Key   string `gorm:"size:50"`
	Value string `gorm:"size:200"`
}

// TestPrimaryKeyColumn_SingleSource（gormx#247）：主键列名解析收敛到
// PrimaryKeyColumn 单源后，既有承诺不变——不要求主键列名为 "id"。
func TestPrimaryKeyColumn_SingleSource(t *testing.T) {
	db := newTestDB(t, &singletonCfgPK{}, &TestBase{})

	// 主键列名非 id 的模型解析为真实列名（与 SingletonRepo 既有回归同口径）
	col, err := PrimaryKeyColumn(db, &singletonCfgPK{})
	if err != nil {
		t.Fatalf("PrimaryKeyColumn(singletonCfgPK): %v", err)
	}
	if col != "uid" {
		t.Errorf("expected pk column 'uid', got %q", col)
	}

	// id 列名模型解析为 "id"
	colID, err := PrimaryKeyColumn(db, &TestBase{})
	if err != nil {
		t.Fatalf("PrimaryKeyColumn(TestBase): %v", err)
	}
	if colID != "id" {
		t.Errorf("expected pk column 'id', got %q", colID)
	}

	// 值类型与指针类型入参等价（各调用点既有形态：var model T / new(M) / entity）
	colVal, err := PrimaryKeyColumn(db, singletonCfgPK{})
	if err != nil {
		t.Fatalf("PrimaryKeyColumn(value): %v", err)
	}
	if colVal != "uid" {
		t.Errorf("value-typed model: expected 'uid', got %q", colVal)
	}
}

// TestPrimaryKeyField_CompositePassthrough 复合主键透传语义：返回优先化
// 主键字段而非报错，复合主键校验是调用方语义（经 Schema 回指判定）——
// 这是单源函数与 5 处调用点的差异分工契约（SavePatch 拒绝、其余路径取
// 优先化主键）。
func TestPrimaryKeyField_CompositePassthrough(t *testing.T) {
	db := newTestDB(t)

	field, err := PrimaryKeyField(db, &pkComposite{})
	if err != nil {
		t.Fatalf("PrimaryKeyField(composite): %v", err)
	}
	if field.DBName != "id" {
		t.Errorf("PrioritizedPrimaryField.DBName = %q, want 'id'", field.DBName)
	}
	if got := len(field.Schema.PrimaryFields); got != 2 {
		t.Errorf("Schema.PrimaryFields = %d 列, want 2（复合主键校验留调用方）", got)
	}
}

// TestPrimaryKeyField_Errors 失败形态：无主键列 / 不可解析的 model 显式报错，
// 不静默——静默侧（applyPrimaryKeyTiebreak）由调用方自行决定忽略并注释。
func TestPrimaryKeyField_Errors(t *testing.T) {
	db := newTestDB(t)

	// 无主键列（也无 id 字段可自动晋升）
	_, err := PrimaryKeyColumn(db, &pkNoPK{})
	if err == nil || !strings.Contains(err.Error(), "no primary key column") {
		t.Fatalf("无主键模型应报 no primary key column, got %v", err)
	}

	// 非结构体 model 不可解析
	_, err = PrimaryKeyColumn(db, 42)
	if err == nil {
		t.Fatal("非 struct model 应报解析错误")
	}
	if !strings.Contains(err.Error(), "parse model schema") {
		t.Errorf("解析错误应带单源包装上下文, got %v", err)
	}
}

// TestSavePatch_CompositePKRejected SavePatch 的复合主键语义约束保留在
// patch 侧（gormx#247 单源改造后经 field.Schema.PrimaryFields 判定）：
// 复合主键模型显式报错，不静默取第一列主键。
func TestSavePatch_CompositePKRejected(t *testing.T) {
	db := newTestDB(t)

	type pkCompositePatch struct {
		Name *string
	}
	err := SavePatch[pkComposite](t.Context(), db, int64(1), &pkCompositePatch{Name: strp("x")})
	if err == nil || !strings.Contains(err.Error(), "复合主键") {
		t.Fatalf("复合主键模型应显式报错, got %v", err)
	}
}
