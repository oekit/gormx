package datatypes

import (
	"database/sql"
	"encoding/json"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// newDatatypesDB 打开 sqlite 内存库并迁移模型（测试专用；glebarez 只在
// _test 文件出现，不构成生产代码的驱动链接）。
func newDatatypesDB(t *testing.T, models ...any) *gorm.DB {
	t.Helper()
	dsn := "file:" + uuid.New().String() + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(models...); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		sqlDB, _ := db.DB()
		_ = sqlDB.Close()
	})
	return db
}

// dtModel 覆盖消费方实证的形态：目标渠道 ID 列表（octopus agent_session
// 的 TargetChannelIdsJSON 形态）、字符串切片、字符串键 map、整数键 map。
type dtModel struct {
	gorm.Model
	ChannelIDs JSONSlice[int64]
	Tags       JSONSlice[string]
	Meta       JSONMap[string, string]
	Weights    JSONMap[string, int]
}

func TestJSONTypes_RoundTrip(t *testing.T) {
	db := newDatatypesDB(t, &dtModel{})

	in := &dtModel{
		ChannelIDs: JSONSlice[int64]{7, 8, 9},
		Tags:       JSONSlice[string]{"a", "b"},
		Meta:       JSONMap[string, string]{"env": "dev", "team": "infra"},
		Weights:    JSONMap[string, int]{"p0": 1, "p1": 5},
	}
	if err := db.Create(in).Error; err != nil {
		t.Fatalf("create: %v", err)
	}

	var got dtModel
	if err := db.First(&got, in.ID).Error; err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(got.ChannelIDs) != 3 || got.ChannelIDs[0] != 7 || got.ChannelIDs[2] != 9 {
		t.Errorf("ChannelIDs = %v", got.ChannelIDs)
	}
	if len(got.Tags) != 2 || got.Tags[1] != "b" {
		t.Errorf("Tags = %v", got.Tags)
	}
	if got.Meta["env"] != "dev" || got.Meta["team"] != "infra" || len(got.Meta) != 2 {
		t.Errorf("Meta = %v", got.Meta)
	}
	if got.Weights["p1"] != 5 || len(got.Weights) != 2 {
		t.Errorf("Weights = %v", got.Weights)
	}
}

func TestJSONTypes_NullVsEmptyDistinct(t *testing.T) {
	db := newDatatypesDB(t, &dtModel{})

	// nil 切片 / nil map 写 NULL；空集合写 "[]"/"{}"——两形态可区分
	empty := &dtModel{
		ChannelIDs: JSONSlice[int64]{},
		Tags:       nil,
		Meta:       JSONMap[string, string]{},
		Weights:    nil,
	}
	if err := db.Create(empty).Error; err != nil {
		t.Fatalf("create: %v", err)
	}
	// 裸 SQL 取存储原值（NULL/文档形态），绕开 gorm 的行扫描类型面
	var rawTags, rawWeights sql.NullString
	row := db.Raw("SELECT tags, weights FROM dt_models WHERE id = ?", empty.ID).Row()
	if err := row.Scan(&rawTags, &rawWeights); err != nil {
		t.Fatal(err)
	}
	if rawTags.Valid {
		t.Errorf("nil 切片应写 NULL, got %q", rawTags.String)
	}
	if rawWeights.Valid {
		t.Errorf("nil map 应写 NULL, got %q", rawWeights.String)
	}
	var rawChannelIDs sql.NullString
	row = db.Raw("SELECT channel_ids FROM dt_models WHERE id = ?", empty.ID).Row()
	if err := row.Scan(&rawChannelIDs); err != nil {
		t.Fatal(err)
	}
	if !rawChannelIDs.Valid || strings.TrimSpace(rawChannelIDs.String) != "[]" {
		t.Errorf("空切片应写 \"[]\", got %+v", rawChannelIDs)
	}

	// 读回：NULL 列 → nil，空集合列 → 空切片/空 map
	var got dtModel
	if err := db.First(&got, empty.ID).Error; err != nil {
		t.Fatal(err)
	}
	if got.Tags != nil || got.Weights != nil {
		t.Errorf("NULL 列应读回 nil, got tags=%v weights=%v", got.Tags, got.Weights)
	}
	if got.ChannelIDs == nil || len(got.ChannelIDs) != 0 {
		t.Errorf("空集合列应读回空切片（非 nil）, got %v", got.ChannelIDs)
	}
	if got.Meta == nil || len(got.Meta) != 0 {
		t.Errorf("空对象列应读回空 map（非 nil）, got %v", got.Meta)
	}
}

// TestJSONTypes_ScanFailLoud 非法 JSON 显式报错（不吞零值）——坏数据
// 漂移成「空值」会让调用方无从察觉（octopus 两种口径并存正是历史形态）。
func TestJSONTypes_ScanFailLoud(t *testing.T) {
	db := newDatatypesDB(t, &dtModel{})
	if err := db.Exec("INSERT INTO dt_models (channel_ids, tags, meta, weights) VALUES ('[1,', '[]', '{}', '{}')").Error; err != nil {
		t.Fatal(err)
	}
	var got dtModel
	err := db.First(&got).Error
	if err == nil {
		t.Fatal("非法 JSON 列读回应显式报错")
	}
	if !strings.Contains(err.Error(), "JSONSlice") {
		t.Errorf("错误应带类型上下文, got: %v", err)
	}
}

func TestJSONSlice_ValueMarshalError(t *testing.T) {
	type bad struct{ Ch chan int }
	_, err := JSONSlice[bad]{{Ch: make(chan int)}}.Value()
	if err == nil || !strings.Contains(err.Error(), "JSONSlice") {
		t.Errorf("不可序列化值应显式报错, got: %v", err)
	}
}

func TestJSONMap_ScanUnsupportedSource(t *testing.T) {
	var m JSONMap[string, int]
	if err := m.Scan(42); err == nil {
		t.Error("非 []byte/string 来源应显式报错")
	}
}

// TestJSONTypes_DirectJSONEncoding 底层类型即切片/map：API 层序列化
// 不需要任何包装（[]T 直接 marshal 成数组）。
func TestJSONTypes_DirectJSONEncoding(t *testing.T) {
	s := JSONSlice[string]{"x", "y"}
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != `["x","y"]` {
		t.Errorf("slice 应按数组原生编解码, got %s", b)
	}
	m := JSONMap[string, int]{"a": 1}
	b, err = json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != `{"a":1}` {
		t.Errorf("map 应按对象原生编解码, got %s", b)
	}
}
