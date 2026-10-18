// Package gormrepo 提供 github.com/oekit/gormx/crud 仓储接口的 GORM 实现。
//
// crud 包保持 ORM 无关（接口 + 中性 Query 构建器），本包把
// QuerySpec 翻译为 gorm.DB 语句——import crud 不再传递编译依赖 GORM。
//
//	import (
//		"github.com/oekit/gormx/crud"
//		gormrepo "github.com/oekit/gormx/gormrepo"
//	)
//
//	repo := gormrepo.NewGormRepository[Project, int64](db)
//	svc := crud.NewBaseService[Project, int64, CreateReq, UpdateReq](repo)
//
// 多租户/行级隔离可挂查询装饰器（scope 自动注入，含按 ID 路径）：
//
//	repo := gormrepo.NewGormRepository[Project, int64](db,
//	    gormrepo.WithQueryDecorator[Project](func(ctx context.Context) func(*gorm.DB) *gorm.DB {
//	        uid := identity.UID(ctx)
//	        return func(db *gorm.DB) *gorm.DB { return db.Where("uid = ?", uid) }
//	    }),
//	    gormrepo.WithScopeRequired[Project](), // fail-closed
//	)
package gormrepo

import (
	"context"
	"errors"
	"fmt"

	"github.com/oekit/gormx"
	"github.com/oekit/gormx/crud"
	"github.com/oekit/pkg/pagination"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ErrNotFound 透传 crud.ErrNotFound（哨兵定义在根包，Service 层依赖它判 404）。
var ErrNotFound = crud.ErrNotFound

// GormRepository 是基于 GORM 的仓储实现。
// ID 为实体主键类型（int64 自增 / string 文本主键等，见 crud.Repository）。
// 查询装饰器（WithQueryDecorator）配置在实例上，RunInTx / WithTx 产出的
// 副本继承同一配置——「事务 + 行锁读」路径同样自动注入 scope。
type GormRepository[T any, ID comparable] struct {
	db  *gorm.DB
	cfg gormRepoConfig[T]
}

// NewGormRepository 创建 GORM 仓储实例。
// opts 省略时为纯 passthrough（单租户零负担）；多租户见 WithQueryDecorator /
// WithScopeRequired。
func NewGormRepository[T any, ID comparable](db *gorm.DB, opts ...RepositoryOption[T]) *GormRepository[T, ID] {
	cfg := gormRepoConfig[T]{}
	for _, o := range opts {
		o(&cfg)
	}
	return &GormRepository[T, ID]{db: db, cfg: cfg}
}

// Create 创建记录。
// 不套查询装饰器（插入路径的身份注入属于 model hook，见 GORM BeforeCreate）。
func (r *GormRepository[T, ID]) Create(ctx context.Context, entity *T) error {
	return r.db.WithContext(ctx).Create(entity).Error
}

// BatchCreate 批量创建记录。同 Create 不套查询装饰器。
func (r *GormRepository[T, ID]) BatchCreate(ctx context.Context, entities []*T, batchSize int) error {
	if batchSize <= 0 {
		batchSize = 100
	}
	return r.db.WithContext(ctx).CreateInBatches(entities, batchSize).Error
}

// RunInTx 在事务中执行 fn（TxRepository 能力）。
// fn 收到的事务仓储继承本实例的查询装饰器配置——scope 在 tx 之上应用，
// 「事务 + 行锁读」路径不会丢 scope。
func (r *GormRepository[T, ID]) RunInTx(ctx context.Context, fn func(repo crud.Repository[T, ID]) error) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return fn(r.WithTx(tx))
	})
}

// GetByIDForUpdate 按 ID 行锁读取（TxRepository 能力）。
// sqlite 方言跳过 FOR UPDATE 子句：dialector 不支持该语法会直接报
// 语法错误，且 sqlite 单写者模型下事务天然串行，锁无增量价值。
// 经 GetOne 走查询装饰器（行锁读同样自动注入 scope）。
// 注意：经中性 Query 表达，主键列名为 "id"（自定义主键列名的实体请以
// GetOne + 显式条件替代）。
func (r *GormRepository[T, ID]) GetByIDForUpdate(ctx context.Context, id ID) (*T, error) {
	q := crud.NewQuery().Where("id", "=", id)
	if r.db.Name() != "sqlite" {
		q = q.ForUpdate()
	}
	return r.GetOne(ctx, q)
}

// GetByID 根据 ID 获取记录。
// 主键条件经 clause.PrimaryColumn 由 schema 解析（不要求主键列名为 "id"，
// 文本主键值不会被误解析为 SQL 片段——GORM 对内联 string 参数按条件
// 语句处理，非数字即注入面，故按 ID 路径一律走 clause 而非内联值）。
// 自动注入查询装饰器（按主键路径最常见的越权缺口，必须过 scope）。
func (r *GormRepository[T, ID]) GetByID(ctx context.Context, id ID) (*T, error) {
	db, err := r.scopedDB(ctx)
	if err != nil {
		return nil, err
	}
	var entity T
	if err := db.Where(clause.Eq{Column: clause.PrimaryColumn, Value: id}).First(&entity).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("%w: id=%v", crud.ErrNotFound, id)
		}
		return nil, err
	}
	return &entity, nil
}

// GetOne 根据查询条件获取单条记录。
func (r *GormRepository[T, ID]) GetOne(ctx context.Context, q *crud.Query) (*T, error) {
	db, err := r.scopedDB(ctx)
	if err != nil {
		return nil, err
	}
	if q != nil {
		db = ApplyQuery(db, q)
	}
	var entity T
	if err := db.First(&entity).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, crud.ErrNotFound
		}
		return nil, err
	}
	return &entity, nil
}

// GetByCondition 根据原始 where 条件获取单条记录。
func (r *GormRepository[T, ID]) GetByCondition(ctx context.Context, query any, args ...any) (*T, error) {
	db, err := r.scopedDB(ctx)
	if err != nil {
		return nil, err
	}
	var entity T
	if err := db.Where(query, args...).First(&entity).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, crud.ErrNotFound
		}
		return nil, err
	}
	return &entity, nil
}

// List 查询列表并返回分页结果。
// total 在不带分页的条件下统计，分页仅作用于列表查询。
// 带 GroupBy 时 total 为分组数（对分组结果做子查询计数）——
// 直接 COUNT(*) 会按组聚合、GORM 只读第一行，total 退化为第一组的行数。
// 计数与列表语句均注入查询装饰器。
func (r *GormRepository[T, ID]) List(ctx context.Context, q *crud.Query) ([]T, int64, error) {
	var total int64
	// Count 必须在不带 OFFSET/LIMIT 的语句上执行：
	// OFFSET 会跳过聚合行，page>=2 时 total 恒为 0
	if q != nil && q.Spec().GroupBy != "" {
		// GroupBy 语句：子查询对分组结果计数（total = 分组数，与列表语义一致）
		subBase, err := r.scopedDB(ctx)
		if err != nil {
			return nil, 0, err
		}
		sub := countStatement(subBase.Model(new(T)), q, true)
		cntDB, err := r.scopedDB(ctx)
		if err != nil {
			return nil, 0, err
		}
		if err := cntDB.Table("(?) AS grouped_count", sub).Count(&total).Error; err != nil {
			return nil, 0, err
		}
	} else {
		countDB, err := r.scopedDB(ctx)
		if err != nil {
			return nil, 0, err
		}
		if q != nil {
			countDB = countStatement(countDB, q, false)
		}
		if err := countDB.Model(new(T)).Count(&total).Error; err != nil {
			return nil, 0, err
		}
	}

	listDB, err := r.scopedDB(ctx)
	if err != nil {
		return nil, 0, err
	}
	if q != nil {
		listDB = ApplyQuery(listDB, q)
	}
	var list []T
	if err := listDB.Find(&list).Error; err != nil {
		return nil, 0, err
	}

	return list, total, nil
}

// Update 更新记录。
// 自动注入查询装饰器（按主键路径：漏 scope = 越权改他行）。
//
// 不用 gorm.Save：Save 在 UPDATE 命中 0 行时回退 upsert
// （INSERT ... ON CONFLICT DO UPDATE），完全绕过 scope WHERE，多租户下
// 等同越权写旁路。改用 Select("*") + Updates 全字段更新，scope WHERE
// 与主键条件合并且无 upsert 旁路（0 行即结束，不插入）。
func (r *GormRepository[T, ID]) Update(ctx context.Context, entity *T) error {
	db, err := r.scopedDB(ctx)
	if err != nil {
		return err
	}
	return db.Model(entity).Select("*").Updates(entity).Error
}

// UpdateFields 根据 ID 更新指定字段。
// 主键列由 GORM schema 解析（不要求主键列名为 "id"）。
// 自动注入查询装饰器。
func (r *GormRepository[T, ID]) UpdateFields(ctx context.Context, id ID, fields map[string]any) error {
	db, err := r.scopedDB(ctx)
	if err != nil {
		return err
	}
	var entity T
	return db.Model(&entity).
		Where(clause.Eq{Column: clause.PrimaryColumn, Value: id}).
		Updates(fields).Error
}

// Delete 根据 ID 删除记录。
// 主键条件经 clause.PrimaryColumn 由 schema 解析（同 GetByID，不走内联值）。
// 自动注入查询装饰器（按主键路径：漏 scope = 越权删他行）。
func (r *GormRepository[T, ID]) Delete(ctx context.Context, id ID) error {
	db, err := r.scopedDB(ctx)
	if err != nil {
		return err
	}
	var entity T
	return db.Where(clause.Eq{Column: clause.PrimaryColumn, Value: id}).Delete(&entity).Error
}

// BatchDelete 批量删除记录。自动注入查询装饰器。
func (r *GormRepository[T, ID]) BatchDelete(ctx context.Context, ids []ID) error {
	db, err := r.scopedDB(ctx)
	if err != nil {
		return err
	}
	var entity T
	return db.Where(clause.IN{Column: clause.PrimaryColumn, Values: anyIDs(ids)}).Delete(&entity).Error
}

// anyIDs 把 []ID 装箱为 []any（clause.IN.Values 形态）。
func anyIDs[ID comparable](ids []ID) []any {
	out := make([]any, len(ids))
	for i, id := range ids {
		out[i] = id
	}
	return out
}

// Exists 检查记录是否存在。
// 分页条件不参与判断（存在性语义与页码无关）。
func (r *GormRepository[T, ID]) Exists(ctx context.Context, q *crud.Query) (bool, error) {
	db, err := r.scopedDB(ctx)
	if err != nil {
		return false, err
	}
	var count int64
	if q != nil {
		// countStatement 剔除排序与 FOR UPDATE:聚合语句带锁子句在
		// PostgreSQL 直接报错、SQLite 语法错误,MySQL 无意义锁行
		db = countStatement(db, q, false)
	}
	if err := db.Model(new(T)).Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

// Count 统计记录数。
// 分页条件不参与统计（计数语义与页码无关）。
func (r *GormRepository[T, ID]) Count(ctx context.Context, q *crud.Query) (int64, error) {
	db, err := r.scopedDB(ctx)
	if err != nil {
		return 0, err
	}
	var count int64
	if q != nil {
		// 同 Exists:计数语句不带排序/悲观锁/GroupBy(见 countStatement 注释)
		db = countStatement(db, q, false)
	}
	if err := db.Model(new(T)).Count(&count).Error; err != nil {
		return 0, err
	}
	return count, nil
}

// DB 获取底层 gorm.DB。
// ⚠️ 绕过查询装饰器：仅用于确实需要裸语句的场景（迁移、管理脚本），
// 业务查询请走仓储方法，否则 scope 自动注入失效。
func (r *GormRepository[T, ID]) DB() *gorm.DB {
	return r.db
}

// WithTx 返回绑定到指定事务的仓储副本。
// 返回具体类型以保留 DB() 等 GormRepository 特有方法；
// 查询装饰器配置随副本继承（scope 在 tx 之上应用）。
func (r *GormRepository[T, ID]) WithTx(tx *gorm.DB) *GormRepository[T, ID] {
	return &GormRepository[T, ID]{db: tx, cfg: r.cfg}
}

// PaginateList 使用 gormx.Paginate 执行分页查询。
// 包级函数无仓储配置，**不**自动注入查询装饰器——需要 scope 时请用
// Repository.List 或先把 scope 合入传入的 db。
func PaginateList[T any](db *gorm.DB, req *pagination.PageRequest, q *crud.Query) (*crud.PageResponse[T], error) {
	resp, err := gormx.Paginate[T](db, req, func(db *gorm.DB) *gorm.DB {
		if q != nil {
			return ApplyQuery(db, q)
		}
		return db
	})
	if err != nil {
		return nil, err
	}
	// gormx.PageResponse 已与 crud.PageResponse 统一为同一类型，直接返回
	return resp, nil
}
