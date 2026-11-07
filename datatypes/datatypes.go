// Package datatypes 提供泛型 JSON 列类型（#80）：结构化 JSON 列
// （[]uint / []string / map[K]V 等）的 Scan/Value 一次到位，替代
// 「每个 model 裸 string 字段 + Get/Set + json.Marshal/Unmarshal +
// 失败吞 nil」的五件套手搓——样板在每个消费方重复拷贝，且反序列化失败
// 语义各自为政（有的返 nil 有的返空切片）。
//
// # 失败语义（显式约定，不在类型里隐式选）
//
//   - Scan 遇到非法 JSON / 非文本来源：返回 error 交给 GORM 报错（行扫描
//     失败显式可见），不吞零值——吞零值会把坏数据静默漂移成「空值」，
//     调用方无从察觉；
//   - Value 遇到不可序列化值（chan 等）：返回 error，同上显式；
//   - NULL 列 Scan 回 nil 零值（JSONSlice[T](nil) / JSONMap[K,V](nil)），
//     与「空数组/空对象」可区分（NULL=从未写入，空=显式写过空集合）。
//
// # 与 GORM 官方 datatypes.JSON 的关系
//
// 官方类型只到 json.RawMessage 级（调用方仍要自己 Unmarshal 到目标类型、
// 自己处理失败）；本包把「目标类型 + 序列化边界」收进列类型本体，
// 值域由泛型参数钉死。
//
// 用法：
//
//	type Channel struct {
//	    gormx.BaseModel
//	    Tags   datatypes.JSONSlice[string] // []string 的 JSON 列
//	    Labels datatypes.JSONMap[string]int
//	}
//	ch.Tags = []string{"a", "b"} // 直接赋底层类型
//	db.Create(&ch)
//	// 读回：ch.Tags 已是 []string{"a","b"}；列类型零 API 面包参与
package datatypes

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"

	"gorm.io/gorm"
	"gorm.io/gorm/schema"
)

// JSONSlice[T] 是 []T 的 JSON 列类型：写入时整切片 marshal 为 JSON 文档，
// 读回时 unmarshal 为 []T。零值（nil 切片）写入 NULL，NULL 列读回 nil——
// 与空切片（写入 "[]"）可区分。
//
// 底层类型就是 []T：调用方可直接赋值/迭代/比较，不需要任何 Get/Set
// 包装方法；encoding/json 对本类型按切片原生编解码（API 序列化同样直接）。
type JSONSlice[T any] []T

// Value 实现 driver.Valuer：nil 写 NULL，其余写 JSON 文档（[]byte）。
// 序列化失败（T 含不可序列化类型）返回 error，由 GORM 显式报错。
func (s JSONSlice[T]) Value() (driver.Value, error) {
	if s == nil {
		return nil, nil
	}
	b, err := json.Marshal([]T(s))
	if err != nil {
		return nil, fmt.Errorf("datatypes.JSONSlice: marshal: %w", err)
	}
	return b, nil
}

// Scan 实现 sql.Scanner：NULL 置 nil；[]byte/string 按文档 unmarshal；
// 非法 JSON 返回 error（不吞零值），其他来源类型同样显式报错。
func (s *JSONSlice[T]) Scan(src any) error {
	if src == nil {
		*s = nil
		return nil
	}
	var b []byte
	switch v := src.(type) {
	case []byte:
		b = v
	case string:
		b = []byte(v)
	default:
		return fmt.Errorf("datatypes.JSONSlice: unsupported source type %T", src)
	}
	if len(b) == 0 {
		*s = nil
		return nil
	}
	var out []T
	if err := json.Unmarshal(b, &out); err != nil {
		return fmt.Errorf("datatypes.JSONSlice: unmarshal %q: %w", string(b), err)
	}
	*s = out
	return nil
}

// GormDataType 声明字段数据类型（GORM schema 解析用；缺了它切片/map 类型
// 会被当成关联或判为不支持）。"json" 与 gorm 官方 datatypes.JSON 同口径。
func (JSONSlice[T]) GormDataType() string { return "json" }

// GormDBDataType 声明 AutoMigrate 的列类型：MySQL 用原生 JSON、PG 用
// JSONB（可索引/可查询），其余方言（sqlite 等）用 TEXT 存储 JSON 文档。
func (JSONSlice[T]) GormDBDataType(db *gorm.DB, field *schema.Field) string {
	return jsonColumnType(db)
}

// JSONMap[K, V] 是 map[K]V 的 JSON 列类型：语义与 JSONSlice 相同
// （nil 写 NULL 读回 nil；失败显式报错），底层类型就是 map[K]V。
//
// K 需为 json 可编码的键类型（string 或整数键——encoding/json 原生支持）。
type JSONMap[K comparable, V any] map[K]V

// Value 实现 driver.Valuer：nil 写 NULL，其余写 JSON 文档。
func (m JSONMap[K, V]) Value() (driver.Value, error) {
	if m == nil {
		return nil, nil
	}
	b, err := json.Marshal(map[K]V(m))
	if err != nil {
		return nil, fmt.Errorf("datatypes.JSONMap: marshal: %w", err)
	}
	return b, nil
}

// Scan 实现 sql.Scanner：NULL 置 nil；非法 JSON 显式报错（不吞零值）。
func (m *JSONMap[K, V]) Scan(src any) error {
	if src == nil {
		*m = nil
		return nil
	}
	var b []byte
	switch v := src.(type) {
	case []byte:
		b = v
	case string:
		b = []byte(v)
	default:
		return fmt.Errorf("datatypes.JSONMap: unsupported source type %T", src)
	}
	if len(b) == 0 {
		*m = nil
		return nil
	}
	var out map[K]V
	if err := json.Unmarshal(b, &out); err != nil {
		return fmt.Errorf("datatypes.JSONMap: unmarshal %q: %w", string(b), err)
	}
	*m = out
	return nil
}

// GormDataType 声明字段数据类型（同 JSONSlice）。
func (JSONMap[K, V]) GormDataType() string { return "json" }

// GormDBDataType 声明 AutoMigrate 的列类型（同 JSONSlice）。
func (JSONMap[K, V]) GormDBDataType(db *gorm.DB, field *schema.Field) string {
	return jsonColumnType(db)
}

// jsonColumnType 按方言返回 JSON 列的 DDL 类型。
func jsonColumnType(db *gorm.DB) string {
	switch db.Name() {
	case "mysql":
		return "JSON"
	case "postgres":
		return "JSONB"
	default:
		// sqlite 等：TEXT 存 JSON 文档（sqlite 的 JSON 函数照常可用）
		return "TEXT"
	}
}
