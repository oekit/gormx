package gormx

import (
	"context"
	"fmt"
	"reflect"

	"gorm.io/gorm"
	gclause "gorm.io/gorm/clause"
)

// SavePatch 用「指针字段补丁」执行部分更新（#105）：patch 结构体里**非 nil**
// 的指针字段才是要写入的列，nil 指针字段 = 未触碰（不参与 UPDATE 语句）。
//
// # 为什么不能用 Save / Updates(struct)
//
//   - Save 是**全列写入**（零值也写）：只传一个可选字段会把其余列清零——
//     JSON 列被写成空串撞 DB 约束（MySQL 4025），varchar 列悄悄清成空串
//     丢用户数据（fuxi config_repo 实测形态）；
//   - Updates(struct) 只写非零字段：**显式零值写不进去**（status 归零、开关
//     关闭这类合法语义无法表达）。
//
// 指针字段补丁两个语义都精确：nil = 跳过，非 nil（哪怕指向零值）= 显式写入。
//
// # 用法
//
//	type ConfigPatch struct { // 与 API 层可选字段（*string/*int）自然对齐
//	    Name   *string
//	    Status *int
//	}
//	err := gormx.SavePatch[Config](ctx, db, id, &ConfigPatch{Name: ptr("new"), Status: ptr(0)})
//
// # 契约
//
//   - patch 的字段名必须与模型字段名一致（列名经 GORM schema 解析，
//     不硬编码、不要求字段名 = 列名）；
//   - patch 字段必须全为指针类型：nil 跳过、非 nil 写入；非指针字段直接
//     报错（它表达不了「未提供」，是调用方的形状错误）；
//   - 模型需有单列主键（复合主键不支持，显式报错）；Updates 语义下
//     gorm 自动维护 UpdatedAt（如模型有）；
//   - RowsAffected 语义：返回的 error 为 nil 即语句成功。**不**从
//     RowsAffected==0 推断 not-found——多数驱动按「值实际变化」计数，
//     值未变化的命中行也计 0；需要 not-found 语义的调用方先读后判。
func SavePatch[M any](ctx context.Context, db *gorm.DB, pk any, patch any) error {
	if db == nil {
		return fmt.Errorf("gormx: SavePatch requires non-nil db")
	}
	if patch == nil {
		return fmt.Errorf("gormx: SavePatch requires non-nil patch")
	}

	updates, err := patchUpdates[M](db, patch)
	if err != nil {
		return err
	}
	if len(updates) == 0 {
		return fmt.Errorf("gormx: SavePatch: patch %T 未提供任何非 nil 字段（全部跳过 = 无语句可执行）", patch)
	}

	pkColumn, err := primaryKeyColumnOf[M](db)
	if err != nil {
		return err
	}

	res := db.WithContext(ctx).Model(new(M)).
		Where(gclause.Eq{Column: gclause.Column{Name: pkColumn}, Value: pk}).
		Updates(updates)
	return res.Error
}

// patchUpdates 把指针字段补丁物化为 {列名: 值}：nil 跳过、非 nil 取指向值。
// 列名经 GORM schema 解析（patch 字段名 → 模型字段 → DB 列名），
// 对不上名的字段显式报错（拼写错误在写入前暴露，而不是静默丢列）。
func patchUpdates[M any](db *gorm.DB, patch any) (map[string]any, error) {
	v := reflect.ValueOf(patch)
	for v.Kind() == reflect.Ptr {
		if v.IsNil() {
			return nil, fmt.Errorf("gormx: SavePatch: patch must not be nil pointer")
		}
		v = v.Elem()
	}
	if v.Kind() != reflect.Struct {
		return nil, fmt.Errorf("gormx: SavePatch: patch must be a struct or pointer to struct, got %T", patch)
	}

	schemaFields, err := modelFields[M](db)
	if err != nil {
		return nil, err
	}

	t := v.Type()
	updates := make(map[string]any, t.NumField())
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		if !field.IsExported() {
			return nil, fmt.Errorf("gormx: SavePatch: patch 字段 %s 未导出（补丁字段必须全部导出）", field.Name)
		}
		fv := v.Field(i)
		if fv.Kind() != reflect.Ptr {
			return nil, fmt.Errorf("gormx: SavePatch: patch 字段 %s 必须为指针类型（nil=不触碰，非 nil=显式写入，含零值）", field.Name)
		}
		if fv.IsNil() {
			continue
		}
		column, ok := schemaFields[field.Name]
		if !ok {
			return nil, fmt.Errorf("gormx: SavePatch: patch 字段 %s 在模型 %T 中无同名字段", field.Name, new(M))
		}
		updates[column] = fv.Elem().Interface()
	}
	return updates, nil
}

// primaryKeyColumnOf 解析模型 M 的主键列名（单主键；复合主键显式报错）。
func primaryKeyColumnOf[M any](db *gorm.DB) (string, error) {
	stmt := &gorm.Statement{DB: db}
	if err := stmt.Parse(new(M)); err != nil {
		return "", fmt.Errorf("gormx: SavePatch: parse model schema: %w", err)
	}
	schema := stmt.Schema
	if len(schema.PrimaryFields) > 1 {
		return "", fmt.Errorf("gormx: SavePatch: 模型 %s 是复合主键（%d 列），SavePatch 不支持", schema.Name, len(schema.PrimaryFields))
	}
	field := schema.PrioritizedPrimaryField
	if field == nil || field.DBName == "" {
		return "", fmt.Errorf("gormx: SavePatch: 模型 %s 无主键列", schema.Name)
	}
	return field.DBName, nil
}

// modelFields 返回模型「字段名 → DB 列名」映射（含嵌入字段的提升字段）。
func modelFields[M any](db *gorm.DB) (map[string]string, error) {
	stmt := &gorm.Statement{DB: db}
	if err := stmt.Parse(new(M)); err != nil {
		return nil, fmt.Errorf("gormx: SavePatch: parse model schema: %w", err)
	}
	out := make(map[string]string, len(stmt.Schema.Fields))
	for _, f := range stmt.Schema.Fields {
		if f.DBName != "" {
			out[f.Name] = f.DBName
		}
	}
	return out, nil
}
