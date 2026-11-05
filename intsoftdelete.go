package gormx

import (
	"reflect"

	"gorm.io/gorm"
	gclause "gorm.io/gorm/clause"
	"gorm.io/gorm/schema"
)

// IntSoftDelete 是整型逻辑删除标记（deleted TINYINT：0=未删 1=已删）。
//
// 与 gorm.DeletedAt（时间戳列）的区别：列类型是整型而非时间戳，
// 零值 0 表示未删 ⇒ 零值实体不会被误判为已删。
//
// 来源：kuboard-go/pkg/db/softdelete.go 的已验证实现上移（matrix 决策 U1）。
type IntSoftDelete int

const (
	// IntSoftDeleteAlive 未删除。
	IntSoftDeleteAlive IntSoftDelete = 0
	// IntSoftDeleteRemoved 已删除。
	IntSoftDeleteRemoved IntSoftDelete = 1
)

// QueryClauses 让所有查询自动带上 `deleted = 0`。
func (d IntSoftDelete) QueryClauses(f *schema.Field) []gclause.Interface {
	return []gclause.Interface{intSoftDeleteQueryClause{Field: f}}
}

// UpdateClauses 让更新操作自动带上 `deleted = 0` 条件。
func (d IntSoftDelete) UpdateClauses(f *schema.Field) []gclause.Interface {
	return []gclause.Interface{intSoftDeleteUpdateClause{Field: f}}
}

// DeleteClauses 把 DELETE 改写成 UPDATE deleted = 1。
func (d IntSoftDelete) DeleteClauses(f *schema.Field) []gclause.Interface {
	return []gclause.Interface{intSoftDeleteDeleteClause{Field: f}}
}

// Scan 实现 sql.Scanner。
func (d *IntSoftDelete) Scan(value any) error {
	switch v := value.(type) {
	case nil:
		*d = IntSoftDeleteAlive
	case int64:
		*d = IntSoftDelete(v)
	case int:
		*d = IntSoftDelete(v)
	case []byte:
		*d = IntSoftDelete(0)
		for _, b := range v {
			if b < '0' || b > '9' {
				*d = IntSoftDeleteAlive
				break
			}
			*d = *d*10 + IntSoftDelete(b-'0')
		}
	default:
		*d = IntSoftDeleteAlive
	}
	return nil
}

// Value 实现 driver.Valuer。
func (d IntSoftDelete) Value() (any, error) { return int(d), nil }

// intSoftDeleteQueryClause 对应 gorm.SoftDeleteQueryClause。
type intSoftDeleteQueryClause struct {
	Field *schema.Field
}

func (c intSoftDeleteQueryClause) Name() string                { return "" }
func (c intSoftDeleteQueryClause) Build(gclause.Builder)       {}
func (c intSoftDeleteQueryClause) MergeClause(*gclause.Clause) {}

func (c intSoftDeleteQueryClause) ModifyStatement(stmt *gorm.Statement) {
	if _, ok := stmt.Clauses["soft_delete_enabled"]; ok || stmt.Statement.Unscoped {
		return
	}
	stmt.AddClause(gclause.Where{Exprs: []gclause.Expression{
		gclause.Eq{Column: gclause.Column{Table: gclause.CurrentTable, Name: c.Field.DBName}, Value: int(IntSoftDeleteAlive)},
	}})
	stmt.Clauses["soft_delete_enabled"] = gclause.Clause{}
}

// intSoftDeleteUpdateClause 对应 gorm.SoftDeleteUpdateClause。
type intSoftDeleteUpdateClause struct {
	Field *schema.Field
}

func (c intSoftDeleteUpdateClause) Name() string                { return "" }
func (c intSoftDeleteUpdateClause) Build(gclause.Builder)       {}
func (c intSoftDeleteUpdateClause) MergeClause(*gclause.Clause) {}

func (c intSoftDeleteUpdateClause) ModifyStatement(stmt *gorm.Statement) {
	if stmt.SQL.Len() == 0 && !stmt.Statement.Unscoped {
		intSoftDeleteQueryClause(c).ModifyStatement(stmt)
	}
}

// intSoftDeleteDeleteClause 对应 gorm.SoftDeleteDeleteClause：把 DELETE 变成 UPDATE。
type intSoftDeleteDeleteClause struct {
	Field *schema.Field
}

func (c intSoftDeleteDeleteClause) Name() string                { return "" }
func (c intSoftDeleteDeleteClause) Build(gclause.Builder)       {}
func (c intSoftDeleteDeleteClause) MergeClause(*gclause.Clause) {}

func (c intSoftDeleteDeleteClause) ModifyStatement(stmt *gorm.Statement) {
	if stmt.SQL.Len() == 0 && !stmt.Statement.Unscoped {
		// 与 gorm.SoftDeleteDeleteClause 一致：此时 Delete 回调还没把主键 WHERE
		// 加进来（那段在 ModifyStatement 之后才跑），主键 WHERE 必须自己补——
		// 否则 `db.Delete(&row{ID: 1})` 这类按主键删的调用会因无 WHERE 报
		// ErrMissingWhereClause（UPDATE 重写仍生效）。
		if stmt.Schema != nil {
			_, queryValues := schema.GetIdentityFieldValuesMap(stmt.Context, stmt.ReflectValue, stmt.Schema.PrimaryFields)
			column, values := schema.ToQueryValues(stmt.Table, stmt.Schema.PrimaryFieldDBNames, queryValues)
			if len(values) > 0 {
				stmt.AddClause(gclause.Where{Exprs: []gclause.Expression{gclause.IN{Column: column, Values: values}}})
			}

			if stmt.ReflectValue.CanAddr() && stmt.Dest != stmt.Model && stmt.Model != nil {
				_, queryValues = schema.GetIdentityFieldValuesMap(stmt.Context, reflect.ValueOf(stmt.Model), stmt.Schema.PrimaryFields)
				column, values = schema.ToQueryValues(stmt.Table, stmt.Schema.PrimaryFieldDBNames, queryValues)
				if len(values) > 0 {
					stmt.AddClause(gclause.Where{Exprs: []gclause.Expression{gclause.IN{Column: column, Values: values}}})
				}
			}
		}

		intSoftDeleteQueryClause(c).ModifyStatement(stmt)

		stmt.AddClause(gclause.Set{{
			Column: gclause.Column{Name: c.Field.DBName},
			Value:  int(IntSoftDeleteRemoved),
		}})
		stmt.SetColumn(c.Field.DBName, int(IntSoftDeleteRemoved), true)

		// 把 DELETE 改写成 UPDATE：加 gclause.Update 并用 Update 回调重建 SQL。
		// （对应 gorm.SoftDeleteDeleteClause 的最后两行，缺了它 DELETE 会真的删行）
		stmt.AddClauseIfNotExists(gclause.Update{})
		stmt.Build(stmt.DB.Callback().Update().Clauses...)
	}
}
