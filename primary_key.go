package gormx

import (
	bizerrors "github.com/oekit/pkg/errors"
	"gorm.io/gorm"
	gschema "gorm.io/gorm/schema"
)

// PrimaryKeyField 解析 model 的主键字段（GORM schema 解析，gormx#247 单源）。
//
// 本仓「&gorm.Statement{DB} → Parse → PrioritizedPrimaryField → DBName」
// 曾有 5 份复制（SingletonRepo 的 Once 缓存版、GormRepository 按列名路径、
// GormRepository.Update 主键零值检查、applyPrimaryKeyTiebreak、SavePatch），
// 缓存策略、复合主键校验、失败形态（error vs 静默）彼此漂移，GORM 升级若改
// schema 解析行为要改 5 处——统一收敛到本函数与 [PrimaryKeyColumn] 两个
// 出口，差异由调用方处理：
//
//   - 缓存策略留在调用方（如 SingletonRepo 保留自己的 sync.Once，调用本函数）；
//   - 复合主键校验留在有该语义的调用方：返回的 *Field 带 Schema 回指，
//     经 field.Schema.PrimaryFields 判定（如 SavePatch 显式拒绝复合主键；
//     本层不报错——复合主键模型是否合法由调用方语义决定）；
//   - 解析失败可静默的场景由调用方忽略返回的 error（须显式注释说明为何
//     可忽略，矩阵红线 error 禁 _ 忽略）。
//
// 返回 schema.PrioritizedPrimaryField；model 不可解析（非 struct/nil 等）或
// 无主键列时返回错误。GORM 内部按 DB 缓存 schema，重复调用是缓存命中。
func PrimaryKeyField(db *gorm.DB, model any) (*gschema.Field, error) {
	stmt := &gorm.Statement{DB: db}
	if err := stmt.Parse(model); err != nil {
		return nil, bizerrors.Wrapf(err, "gormx: parse model schema")
	}
	field := stmt.Schema.PrioritizedPrimaryField
	if field == nil || field.DBName == "" {
		return nil, bizerrors.Newf("gormx: model %T has no primary key column", model)
	}
	return field, nil
}

// PrimaryKeyColumn 解析 model 的主键列名（GORM schema 解析，gormx#247 单源）。
// 与 clause.PrimaryColumn 的既有承诺一致：不要求主键列名为 "id"，主键列名
// 非 id 的模型同样可用。需要复合主键校验或主键零值判定（指针/自定义 Valuer
// 走 field.ValueOf）的调用方改用 [PrimaryKeyField] 取字段本体。
func PrimaryKeyColumn(db *gorm.DB, model any) (string, error) {
	field, err := PrimaryKeyField(db, model)
	if err != nil {
		return "", err
	}
	return field.DBName, nil
}
