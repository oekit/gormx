package gormx

import (
	"context"
	"maps"
	"reflect"
	"strings"

	"gorm.io/gorm"
)

// AuditPlugin 是 GORM 审计插件，自动填充 created_by/updated_by 字段。
// 实体必须包含 CreatedBy 和 UpdatedBy 字段。
type AuditPlugin struct {
	// UserIDFunc 从 context 提取当前用户 ID。
	UserIDFunc func(ctx context.Context) int64
}

// NewAuditPlugin 创建审计插件。
// userIDFunc 从 context 提取当前用户 ID 的函数。
func NewAuditPlugin(userIDFunc func(ctx context.Context) int64) *AuditPlugin {
	return &AuditPlugin{UserIDFunc: userIDFunc}
}

// Name 返回插件名称。
func (p *AuditPlugin) Name() string {
	return "audit"
}

// Initialize 初始化插件，注册 GORM 回调。
func (p *AuditPlugin) Initialize(db *gorm.DB) error {
	// 注册创建回调
	if err := db.Callback().Create().Before("gorm:create").Register("audit:before_create", p.beforeCreate); err != nil {
		return err
	}

	// 注册更新回调
	if err := db.Callback().Update().Before("gorm:update").Register("audit:before_update", p.beforeUpdate); err != nil {
		return err
	}

	return nil
}

// beforeCreate 创建前回调，自动填充 created_by。
func (p *AuditPlugin) beforeCreate(tx *gorm.DB) {
	if p.UserIDFunc == nil {
		return
	}

	ctx := tx.Statement.Context
	userID := p.UserIDFunc(ctx)
	if userID == 0 {
		return
	}

	// 通过反射设置 created_by 字段
	setField(tx, "CreatedBy", userID)
}

// beforeUpdate 更新前回调，自动填充 updated_by。
func (p *AuditPlugin) beforeUpdate(tx *gorm.DB) {
	if p.UserIDFunc == nil {
		return
	}

	ctx := tx.Statement.Context
	userID := p.UserIDFunc(ctx)
	if userID == 0 {
		return
	}

	// 通过反射设置 updated_by 字段
	setField(tx, "UpdatedBy", userID)
}

// setField 通过反射设置结构体字段值。
// ReflectValue 为 slice/array（BatchCreate/CreateInBatches 等批量场景）时
// 遍历每个元素设置，避免批量插入的审计字段被静默跳过（空 created_by/updated_by）。
//
// map 形式的 Updates（如 crud 的 UpdateFields）：SET 列表完全来自 map，
// 只改 ReflectValue 对 SQL 无效——审计字段静默缺失。按 schema 查出列名
// 注入**克隆副本**（#268）：GORM 把调用方 map 原样赋给 Statement.Dest，
// 直接写 dest[f.DBName] 会让调用方复用的缓存 map 被并发写（进程级
// fatal error: concurrent map writes），顺序复用时上次注入值也会残留。
// 插件永不修改调用方对象；schema 无该字段（表没有审计列）时跳过，不注入
// 避免 SQL 报错。
//
// 列白名单（db.Select(cols) 受限的 Selects）下，审计列若不在白名单会被
// 静默剔除出 SET（#268）：这里在注入值后把审计列显式追加进 Selects。
func setField(tx *gorm.DB, fieldName string, value int64) {
	st := tx.Statement
	var dbName string
	if st != nil && st.Schema != nil {
		if f := st.Schema.LookUpField(fieldName); f != nil {
			dbName = f.DBName
		}
	}

	if st != nil {
		if dest, ok := st.Dest.(map[string]any); ok {
			// 表没有审计列：跳过注入（同历史语义），且绝不写调用方 map。
			if dbName == "" {
				return
			}
			cloned := maps.Clone(dest)
			if cloned == nil {
				cloned = make(map[string]any, 1)
			}
			cloned[dbName] = value
			st.Dest = cloned
			ensureSelectedAuditColumn(st, dbName, fieldName)
			return
		}
	}

	rv := tx.Statement.ReflectValue
	if rv.Kind() == reflect.Pointer {
		rv = rv.Elem()
	}

	switch rv.Kind() {
	case reflect.Struct:
		if setStructInt64Field(rv, fieldName, value) {
			// 列白名单受限时审计列同样必须进 SET（struct 更新路径，#268）。
			ensureSelectedAuditColumn(st, dbName, fieldName)
		}
	case reflect.Slice, reflect.Array:
		var set bool
		for i := range rv.Len() {
			elem := rv.Index(i)
			if elem.Kind() == reflect.Pointer {
				elem = elem.Elem()
			}
			if elem.Kind() != reflect.Struct {
				continue
			}
			if setStructInt64Field(elem, fieldName, value) {
				set = true
			}
		}
		if set {
			ensureSelectedAuditColumn(st, dbName, fieldName)
		}
	}
}

// ensureSelectedAuditColumn 在语句带列白名单（Statement.Selects 非空）且
// 白名单未包含审计列时把它追加进 Selects（#268）：
// db.Model(entity).Select(cols).Updates(entity) 的 SET 列表只含白名单命中
// 列，插件改了实体内存字段也被静默剔除——审计链系统性空洞且无日志。
// Selects 为空（未受限）或已含该列（DB 列名/字段名/通配 *）时不动。
func ensureSelectedAuditColumn(st *gorm.Statement, dbName, fieldName string) {
	if st == nil || dbName == "" || len(st.Selects) == 0 {
		return
	}
	for _, sel := range st.Selects {
		if sel == "*" || strings.EqualFold(sel, dbName) || strings.EqualFold(sel, fieldName) {
			return
		}
	}
	st.Selects = append(st.Selects, dbName)
}

// setStructInt64Field 设置结构体上审计字段的值（字段不存在或不可设置时跳过）。
// 返回是否实际写入（列白名单追加只在写入成功时发生，#268）。
// #235：此前只认 Kind()==Int64——审计列声明为 int/uint 家族（UserID int 之类）
// 时静默恒 0。全家族覆盖；超字段范围跳过不写（Overflow 检查避免 SetInt panic）。
func setStructInt64Field(rv reflect.Value, fieldName string, value int64) bool {
	fv := rv.FieldByName(fieldName)
	if !fv.IsValid() || !fv.CanSet() {
		return false
	}
	switch fv.Kind() {
	case reflect.Int64:
		fv.SetInt(value)
		return true
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32:
		if fv.OverflowInt(value) {
			return false
		}
		fv.SetInt(value)
		return true
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		if value < 0 || fv.OverflowUint(uint64(value)) {
			return false
		}
		fv.SetUint(uint64(value))
		return true
	}
	return false
}
