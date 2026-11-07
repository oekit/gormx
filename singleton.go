package gormx

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"gorm.io/gorm"
	gclause "gorm.io/gorm/clause"
)

// Model 是 GORM 模型的最小接口，只需暴露主键。
// 与 BaseModel/SoftDeleteModel/AuditModel 的主键类型（int64）保持一致。
type Model interface {
	GetID() int64
	SetID(id int64)
}

// SingletonRepo 提供单例记录的 get-or-default + upsert 模式。
// 适用于全局配置、系统设置等只有一条记录的场景。
//
// 主键列名通过 GORM schema 解析（不硬编码 "id"），主键列名非 id 的模型
// 同样可用；解析失败时 Get/Save/GetOrCreate 返回错误。
//
// 用法:
//
//	type MyConfig struct { gormx.BaseModel; Value string }
//	// MyConfig 通过嵌入 BaseModel 自动获得 GetID/SetID，满足 Model 接口
//
//	repo := gormx.NewSingletonRepo(db, func() *MyConfig { return &MyConfig{Value: "default"} })
//	cfg, err := repo.Get(ctx)
//	cfg.Value = "updated"
//	err = repo.Save(ctx, cfg)
type SingletonRepo[T Model] struct {
	db         *gorm.DB
	defaultVal func() T

	once     sync.Once
	pkColumn string
	pkErr    error
}

// NewSingletonRepo 创建单例仓库。
// defaultVal 返回当记录不存在时的默认值。
func NewSingletonRepo[T Model](db *gorm.DB, defaultVal func() T) *SingletonRepo[T] {
	return &SingletonRepo[T]{db: db, defaultVal: defaultVal}
}

// primaryKeyColumn 解析模型主键列名（GORM schema 解析，懒加载缓存）。
// 与 repository 的 UpdateFields（clause.PrimaryColumn）保持一致的承诺：
// 不要求主键列名为 "id"。
func (r *SingletonRepo[T]) primaryKeyColumn() (string, error) {
	r.once.Do(func() {
		var model T
		stmt := &gorm.Statement{DB: r.db}
		if err := stmt.Parse(model); err != nil {
			r.pkErr = fmt.Errorf("gormx: parse singleton model schema: %w", err)
			return
		}
		field := stmt.Schema.PrioritizedPrimaryField
		if field == nil || field.DBName == "" {
			r.pkErr = fmt.Errorf("gormx: singleton model %T has no primary key column", model)
			return
		}
		r.pkColumn = field.DBName
	})
	return r.pkColumn, r.pkErr
}

// Get 获取唯一的记录。不存在时返回 defaultVal() 的结果，不返回 error。
func (r *SingletonRepo[T]) Get(ctx context.Context) (T, error) {
	var model T
	col, err := r.primaryKeyColumn()
	if err != nil {
		var zero T
		return zero, err
	}
	// 注意：Order 只链在 First 的查询链上，不污染后续语句的 clause 状态。
	db := r.db.WithContext(ctx).
		Order(gclause.OrderByColumn{Column: gclause.Column{Name: col}, Desc: true})
	err = db.First(&model).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return r.defaultVal(), nil
		}
		var zero T
		return zero, err
	}
	return model, nil
}

// Save 保存单例记录。已存在则更新（通过 ID），不存在则创建。
// 在事务中使用 SELECT ... FOR UPDATE 锁定行。注意：FOR UPDATE 锁不住
// "空表并发插入"的幻影（READ COMMITTED 下两个事务都读到 not found 后
// 各自 Create）——Create 撞唯一约束/主键冲突时重读并改走更新路径收敛；
// 单例表建议加唯一约束作为最终防线。
func (r *SingletonRepo[T]) Save(ctx context.Context, model T) error {
	col, err := r.primaryKeyColumn()
	if err != nil {
		return err
	}
	for attempt := 0; attempt < 2; attempt++ {
		err = r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			var existing T
			qerr := tx.Clauses(gclause.Locking{Strength: "UPDATE"}).
				Order(gclause.OrderByColumn{Column: gclause.Column{Name: col}, Desc: true}).
				First(&existing).Error
			if qerr != nil && !errors.Is(qerr, gorm.ErrRecordNotFound) {
				return qerr
			}
			if existing.GetID() > 0 {
				model.SetID(existing.GetID())
				return tx.Save(model).Error
			}
			return tx.Create(model).Error
		})
		if err == nil {
			return nil
		}
		// 并发首插：另一方已 Create，本轮重读命中其记录走 Save 更新。
		// 判据用 IsUniqueViolation（#99）：它覆盖 gorm.ErrDuplicatedKey（开
		// TranslateError 时）**和**各家驱动的裸文案——sqlite（glebarez 纯 Go，
		// 本包测试默认库）/MySQL/PG 未开 TranslateError 时错误是裸字符串，
		// errors.Is(err, gorm.ErrDuplicatedKey) 判不出来，收敛逻辑一次都不
		// 触发，正是该重试存在的理由。
		if attempt == 0 && IsUniqueViolation(err) {
			continue
		}
		break
	}
	return err
}

// GetOrCreate 获取记录，不存在时创建默认值并返回。
// 在事务中使用 SELECT ... FOR UPDATE 锁定行。与 Save 相同：FOR UPDATE
// 锁不住空表并发插入的幻影，Create 撞唯一约束时重读收敛（最多重试一次）；
// 单例表建议加唯一约束作为最终防线。
func (r *SingletonRepo[T]) GetOrCreate(ctx context.Context) (T, error) {
	col, err := r.primaryKeyColumn()
	if err != nil {
		var zero T
		return zero, err
	}
	var result T
	for attempt := 0; attempt < 2; attempt++ {
		err = r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			var model T
			qerr := tx.Clauses(gclause.Locking{Strength: "UPDATE"}).
				Order(gclause.OrderByColumn{Column: gclause.Column{Name: col}, Desc: true}).
				First(&model).Error
			if qerr != nil && !errors.Is(qerr, gorm.ErrRecordNotFound) {
				return qerr
			}
			if model.GetID() > 0 {
				result = model
				return nil
			}
			model = r.defaultVal()
			if cerr := tx.Create(&model).Error; cerr != nil {
				return cerr
			}
			result = model
			return nil
		})
		if err == nil {
			return result, nil
		}
		// 与 Save 同判据（#99）：IsUniqueViolation 含 gorm.ErrDuplicatedKey
		// 分支与 sqlite/MySQL/PG 的裸文案兜底——只认 ErrDuplicatedKey 时
		// sqlite 下（未开 TranslateError）收敛逻辑形同虚设。
		if attempt == 0 && IsUniqueViolation(err) {
			continue
		}
		break
	}
	return result, err
}
