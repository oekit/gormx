package scoper

import (
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// scopedTask 测试模型：UID 所有者列 + is_builtin 内置行标记。
type scopedTask struct {
	gorm.Model
	UID      int64 `gorm:"column:uid;index"`
	Builtin  bool  `gorm:"column:is_builtin"`
	Title    string
	Assigned int64 // 无关列（验证过滤不误伤）
}

func newScoperDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+uuid.New().String()+"?mode=memory&cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&scopedTask{}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		sqlDB, _ := db.DB()
		_ = sqlDB.Close()
	})
	return db
}

func seedScopeRows(t *testing.T, db *gorm.DB) {
	t.Helper()
	rows := []scopedTask{
		{UID: 7, Title: "mine-1"},
		{UID: 7, Title: "mine-2"},
		{UID: 8, Title: "others"},
		{UID: 0, Builtin: true, Title: "builtin"},
	}
	for i := range rows {
		if err := db.Create(&rows[i]).Error; err != nil {
			t.Fatal(err)
		}
	}
}

// builtinRule 装配的 canonical 形态（正例配置）。
func testRule() *Rule[scopedTask] {
	return NewRule[scopedTask]("uid", func(t scopedTask) int64 { return t.UID },
		WithBuiltinRows("is_builtin", true, func(t scopedTask) bool { return t.Builtin }))
}

// TestRule_Matrix 两面一致性矩阵：同一主体下 SQL 面（Find 结果集）与内存面
// （Allows）对每一行的判定必须一致——单源的核心契约。
func TestRule_Matrix(t *testing.T) {
	db := newScoperDB(t)
	seedScopeRows(t, db)
	rule := testRule()

	var all []scopedTask
	if err := db.Find(&all).Error; err != nil {
		t.Fatal(err)
	}

	cases := []Subject{
		{UID: 7, Admin: false},
		{UID: 8, Admin: false},
		{UID: 9, Admin: false},
		{UID: 7, Admin: true},
	}
	for _, sub := range cases {
		var got []scopedTask
		if err := db.Scopes(rule.Scope(sub)).Find(&got).Error; err != nil {
			t.Fatalf("sub %+v: %v", sub, err)
		}
		visible := map[uint]bool{}
		for _, row := range got {
			visible[row.ID] = true
		}
		// 内存面必须与 SQL 面逐行一致
		for _, row := range all {
			if visible[row.ID] != rule.Allows(row, sub) {
				t.Errorf("sub %+v: row %d (%s uid=%d builtin=%v) SQL 面=%v 内存面=%v —— 两面漂移",
					sub, row.ID, row.Title, row.UID, row.Builtin, visible[row.ID], rule.Allows(row, sub))
			}
		}
	}

	// 具体可见性形态锚定：uid=7 非 admin 可见 3 行（2 本人 + 1 内置）
	var n int64
	db.Model(&scopedTask{}).Scopes(testRule().Scope(Subject{UID: 7})).Count(&n)
	if n != 3 {
		t.Errorf("uid=7 非 admin 应见 3 行（2 本人 + 1 内置）, got %d", n)
	}
	db.Model(&scopedTask{}).Scopes(testRule().Scope(Subject{UID: 9})).Count(&n)
	if n != 1 {
		t.Errorf("uid=9 非 admin 应见 1 行（仅内置）, got %d", n)
	}
	db.Model(&scopedTask{}).Scopes(testRule().Scope(Subject{UID: 9, Admin: true})).Count(&n)
	if n != 4 {
		t.Errorf("admin 应全量 4 行, got %d", n)
	}
}

// TestRule_NoBuiltinOwnerOnly 不配 WithBuiltinRows = 仅本人变体
// （对应消费方 ApplyScope 形态）。
func TestRule_NoBuiltinOwnerOnly(t *testing.T) {
	db := newScoperDB(t)
	seedScopeRows(t, db)
	rule := NewRule[scopedTask]("uid", func(t scopedTask) int64 { return t.UID })

	var got []scopedTask
	if err := db.Scopes(rule.Scope(Subject{UID: 7})).Find(&got).Error; err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("仅本人形态应见 2 行, got %d", len(got))
	}
	for _, row := range got {
		if row.UID != 7 {
			t.Errorf("非本人行 %d 泄漏（uid=%d）", row.ID, row.UID)
		}
		if rule.Allows(row, Subject{UID: 7}) != (row.UID == 7) {
			t.Errorf("内存面不一致: row uid=%d", row.UID)
		}
	}
	// 内置行在内存面也不可见（未配置 builtin）
	builtin := scopedTask{UID: 0, Builtin: true}
	if rule.Allows(builtin, Subject{UID: 7}) {
		t.Error("未配置内置行时 builtin 行应不可见")
	}
}

// TestRule_FailClosedOnMisconfig 规则配置不完整时两面 fail-closed：
// SQL 面恒假 1=0（放行 0 行），内存面恒 false。#195 起 builtin 侧误配
// （column 空 / match nil）纳入完整性判定，与 owner 侧同口径——此前
// match nil 在内存面 nil func call 直接 panic、column 空在 SQL 面拼出
// 非法 SQL（500），都不是声明的 fail-closed 形态。
func TestRule_FailClosedOnMisconfig(t *testing.T) {
	db := newScoperDB(t)
	seedScopeRows(t, db)

	for name, rule := range map[string]*Rule[scopedTask]{
		"nil 规则":        nil,
		"空 ownerColumn": NewRule[scopedTask]("", func(t scopedTask) int64 { return t.UID }),
		"nil ownerOf":   NewRule[scopedTask]("uid", nil),
		"builtin 空 column": NewRule[scopedTask]("uid", func(t scopedTask) int64 { return t.UID },
			WithBuiltinRows("", true, func(t scopedTask) bool { return t.Builtin })),
		"builtin nil match": NewRule[scopedTask]("uid", func(t scopedTask) int64 { return t.UID },
			WithBuiltinRows[scopedTask]("is_builtin", true, nil)),
	} {
		var n int64
		db.Model(&scopedTask{}).Scopes(rule.Scope(Subject{UID: 7})).Count(&n)
		if n != 0 {
			t.Errorf("%s: SQL 面应恒假（0 行）, got %d —— fail-open 不可接受", name, n)
		}
		// 本人行：规则整体不完整时 owner 匹配也必须被挡（fail-closed）
		if allowsNoPanic(t, name, rule, scopedTask{UID: 7}, Subject{UID: 7}) {
			t.Errorf("%s: 内存面（本人行）应恒 false", name)
		}
		// 非本人行：走 builtin 分支——nil match 不得 panic（#195 缺陷形态）
		if allowsNoPanic(t, name, rule, scopedTask{UID: 99}, Subject{UID: 7}) {
			t.Errorf("%s: 内存面（非本人行）应恒 false", name)
		}
	}
	// admin 旁路在规则缺失时仍然成立（旁路不依赖规则完整性）
	var n int64
	db.Model(&scopedTask{}).Scopes(testRule().Scope(Subject{UID: 7, Admin: true})).Count(&n)
	if n != 4 {
		t.Errorf("admin 应全量 4 行, got %d", n)
	}
}

// allowsNoPanic 包一层 recover：#195 的缺陷形态之一是 builtin match=nil 在
// 内存面 nil func call 直接 panic——测试面把它转成清晰的失败信息而非整包
// 崩溃（修复后 valid() 拦截，不会走到 match 调用）。
func allowsNoPanic(t *testing.T, name string, rule *Rule[scopedTask], row scopedTask, sub Subject) (got bool) {
	t.Helper()
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("%s: Allows panic（#195：builtin 误配不得在内存面 nil func call）: %v", name, r)
		}
	}()
	return rule.Allows(row, sub)
}

// TestRule_ScopeSQLShape SQL 形态锚定：owner 条件 + 内置行 OR 分支、
// 列名走 gorm 引用（`uid`）。
func TestRule_ScopeSQLShape(t *testing.T) {
	db := newScoperDB(t)
	dry := db.Session(&gorm.Session{DryRun: true})

	tx := dry.Model(&scopedTask{}).Scopes(testRule().Scope(Subject{UID: 7})).Find(&[]scopedTask{})
	sql := tx.Statement.SQL.String()
	if !strings.Contains(sql, "`uid` = ?") {
		t.Errorf("应有 owner 等值条件, got: %s", sql)
	}
	if !strings.Contains(sql, "`is_builtin` = ?") {
		t.Errorf("应有内置行 OR 分支, got: %s", sql)
	}

	tx = dry.Model(&scopedTask{}).Scopes(
		NewRule[scopedTask]("uid", func(t scopedTask) int64 { return t.UID }).Scope(Subject{UID: 7}),
	).Find(&[]scopedTask{})
	sql = tx.Statement.SQL.String()
	if strings.Contains(sql, "is_builtin") {
		t.Errorf("未配置内置行时不应出现 builtin 分支, got: %s", sql)
	}
}
