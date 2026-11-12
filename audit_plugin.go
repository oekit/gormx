package gormx

import (
	"context"
	"reflect"

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
func setField(tx *gorm.DB, fieldName string, value int64) {
	// map 形式的 Updates（如 crud 的 UpdateFields）：SET 列表完全来自 map，
	// 只改 ReflectValue 对 SQL 无效——审计字段静默缺失。按 schema 查出列名
	// 直接注入 map；schema 无该字段（表没有审计列）时跳过，不注入避免 SQL 报错。
	if dest, ok := tx.Statement.Dest.(map[string]any); ok && tx.Statement.Schema != nil {
		if f := tx.Statement.Schema.LookUpField(fieldName); f != nil {
			dest[f.DBName] = value
		}
		return
	}

	rv := tx.Statement.ReflectValue
	if rv.Kind() == reflect.Pointer {
		rv = rv.Elem()
	}

	switch rv.Kind() {
	case reflect.Struct:
		setStructInt64Field(rv, fieldName, value)
	case reflect.Slice, reflect.Array:
		for i := range rv.Len() {
			elem := rv.Index(i)
			if elem.Kind() == reflect.Pointer {
				elem = elem.Elem()
			}
			if elem.Kind() != reflect.Struct {
				continue
			}
			setStructInt64Field(elem, fieldName, value)
		}
	}
}

// setStructInt64Field 设置结构体上指定 int64 字段的值（字段不存在或不可设置时跳过）。
func setStructInt64Field(rv reflect.Value, fieldName string, value int64) {
	fv := rv.FieldByName(fieldName)
	if !fv.IsValid() || !fv.CanSet() {
		return
	}
	if fv.Kind() == reflect.Int64 {
		fv.SetInt(value)
	}
}
