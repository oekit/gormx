package gormx

import (
	"testing"

	"gorm.io/gorm"
	"gorm.io/gorm/schema"
)

// intSDRow 是 IntSoftDelete 的测试模型（deleted TINYINT：0=未删 1=已删）。
type intSDRow struct {
	ID      int           `gorm:"primaryKey"`
	Name    string        `gorm:"column:name"`
	Deleted IntSoftDelete `gorm:"column:deleted;softDelete;default:0"`
}

func (intSDRow) TableName() string { return "t_int_soft_delete" }

func TestIntSoftDeleteValue(t *testing.T) {
	v, err := IntSoftDeleteAlive.Value()
	if err != nil {
		t.Fatalf("Value() error = %v", err)
	}
	if v != 0 {
		t.Fatalf("IntSoftDeleteAlive.Value() = %v, want 0", v)
	}
	v, err = IntSoftDeleteRemoved.Value()
	if err != nil {
		t.Fatalf("Value() error = %v", err)
	}
	if v != 1 {
		t.Fatalf("IntSoftDeleteRemoved.Value() = %v, want 1", v)
	}
}

// Scan 的关键语义：非数字字节串退回「未删」（0）——与既有消费方一致。
func TestIntSoftDeleteScan(t *testing.T) {
	cases := []struct {
		in   any
		want IntSoftDelete
	}{
		{nil, IntSoftDeleteAlive},
		{int64(1), IntSoftDeleteRemoved},
		{int(0), IntSoftDeleteAlive},
		{[]byte("1"), IntSoftDeleteRemoved},
		{[]byte("0"), IntSoftDeleteAlive},
	}
	for _, c := range cases {
		var d IntSoftDelete
		if err := d.Scan(c.in); err != nil {
			t.Fatalf("Scan(%v) error = %v", c.in, err)
		}
		if d != c.want {
			t.Fatalf("Scan(%v) = %v, want %v", c.in, d, c.want)
		}
	}
}

// #231：非法 []byte / 未知驱动类型 fail-closed——此前静默置 Alive，
// 「已删行被读成未删」是数据暴露方向的假阴性。
func TestIntSoftDeleteScanFailClosed(t *testing.T) {
	var d IntSoftDelete
	if err := d.Scan([]byte("xx")); err == nil {
		t.Fatal("非法字节应报错(fail-closed),此前静默置 Alive")
	}
	if err := d.Scan(3.14); err == nil {
		t.Fatal("未知驱动类型应报错(fail-closed)")
	}
}

// 关键语义：零值实体不会被误判为已删；Unscoped 能看到已删行。
func TestIntSoftDeleteZeroValueIsAlive(t *testing.T) {
	db := newTestDB(t, &intSDRow{})
	db.Create(&intSDRow{ID: 1, Name: "a"})
	db.Create(&intSDRow{ID: 2, Name: "b", Deleted: IntSoftDeleteRemoved})

	var got []intSDRow
	if err := db.Find(&got).Error; err != nil {
		t.Fatalf("Find() error = %v", err)
	}
	if len(got) != 1 || got[0].ID != 1 {
		t.Fatalf("scoped Find() got %+v, want only id=1", got)
	}

	var all []intSDRow
	if err := db.Unscoped().Find(&all).Error; err != nil {
		t.Fatalf("Unscoped Find() error = %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("Unscoped Find() got %d rows, want 2", len(all))
	}
}

// 关键语义：Delete 改写成 UPDATE deleted=1，不物理删行。
func TestIntSoftDeleteDeleteBecomesUpdate(t *testing.T) {
	db := newTestDB(t, &intSDRow{})
	db.Create(&intSDRow{ID: 1, Name: "a"})

	if err := db.Delete(&intSDRow{ID: 1}).Error; err != nil {
		t.Fatalf("Delete() error = %v", err)
	}

	var n int64
	db.Unscoped().Model(&intSDRow{}).Where("id = ?", 1).Count(&n)
	if n != 1 {
		t.Fatalf("row physically deleted: count = %d, want 1", n)
	}
	var deleted IntSoftDelete
	db.Unscoped().Model(&intSDRow{}).Where("id = ?", 1).Select("deleted").Scan(&deleted)
	if deleted != IntSoftDeleteRemoved {
		t.Fatalf("deleted = %v, want %v", deleted, IntSoftDeleteRemoved)
	}
}

// 三个子句接口必须实现（GORM 靠类型断言接线，编译期查不出漏实现）。
func TestIntSoftDeleteClausesImplemented(t *testing.T) {
	var d IntSoftDelete
	f := &schema.Field{}
	if got := d.QueryClauses(f); len(got) == 0 {
		t.Fatal("QueryClauses returned empty")
	}
	if got := d.UpdateClauses(f); len(got) == 0 {
		t.Fatal("UpdateClauses returned empty")
	}
	if got := d.DeleteClauses(f); len(got) == 0 {
		t.Fatal("DeleteClauses returned empty")
	}
	var _ = gorm.Statement{} // 确认 gorm 包路径与仓内一致
}
