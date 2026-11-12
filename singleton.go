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
// defaultVal 返回当记录不存在时的默认值；nil 属构造期编程错误，直接 panic
// （gormx#182，与仓内构造期 fail-fast 纪律一致）：懒校验会把故障推迟到
// 「线上空表首次 Get/GetOrCreate」才以 nil func deref panic 爆出——数据
// 依赖的延迟故障，出事点与根因点距离最远、排查成本最高。构造期响亮拒绝
// 后，Save/Get/GetOrCreate 不再各自校验（单一校验点）。
func NewSingletonRepo[T Model](db *gorm.DB, defaultVal func() T) *SingletonRepo[T] {
	if defaultVal == nil {
		panic("gormx: NewSingletonRepo requires a non-nil defaultVal func (empty-table Get/GetOrCreate would lazy-panic, gormx#182)")
	}
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

// runWithUniqueRetry 是 Save / GetOrCreate 共享的事务骨架（#172 收敛单源）：
// 「SELECT ... FOR UPDATE 锁行读（空表按未命中放行）→ fn 事务内策略 →
// 唯一冲突重试一次」的 for-attempt 循环只此一份。
//
// 并发首插幻影：FOR UPDATE 锁不住"空表并发插入"（READ COMMITTED 下两个
// 事务都读到 not found 后各自 Create）——Create 撞唯一约束/主键冲突时
// 重读并改走更新路径收敛，fn 在第二轮会读到对方已插入的行。
//
// 重试判据用 IsUniqueViolation（#99，重报 #112）：它覆盖 gorm.ErrDuplicatedKey
// （开 TranslateError 时）**和**各家驱动的裸文案——sqlite（glebarez 纯 Go，
// 本包测试默认库）/MySQL/PG 未开 TranslateError 时错误是裸字符串，
// errors.Is(err, gorm.ErrDuplicatedKey) 判不出来，收敛逻辑一次都不触发，
// 正是该重试存在的理由。#99/#112 类判据修补只需改这一处。
//
// fn 收到锁行读的 existing（值拷贝；未命中时为主键零值，调用方按
// GetID() > 0 区分）与当前事务句柄 tx；fn 的错误参与同一重试判定。
func (r *SingletonRepo[T]) runWithUniqueRetry(ctx context.Context, col string, fn func(tx *gorm.DB, existing T) error) error {
	var err error
	for attempt := range 2 {
		err = r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			var existing T
			qerr := tx.Clauses(gclause.Locking{Strength: "UPDATE"}).
				Order(gclause.OrderByColumn{Column: gclause.Column{Name: col}, Desc: true}).
				First(&existing).Error
			if qerr != nil && !errors.Is(qerr, gorm.ErrRecordNotFound) {
				return qerr
			}
			return fn(tx, existing)
		})
		if err == nil {
			return nil
		}
		if attempt == 0 && IsUniqueViolation(err) {
			continue
		}
		break
	}
	return err
}

// Save 保存单例记录。已存在则更新（通过 ID），不存在则创建。
// 在事务中使用 SELECT ... FOR UPDATE 锁定行。并发首插的幻影与收敛口径
// 见 runWithUniqueRetry；单例表建议加唯一约束作为最终防线。
func (r *SingletonRepo[T]) Save(ctx context.Context, model T) error {
	col, err := r.primaryKeyColumn()
	if err != nil {
		return err
	}
	return r.runWithUniqueRetry(ctx, col, func(tx *gorm.DB, existing T) error {
		if existing.GetID() > 0 {
			model.SetID(existing.GetID())
			return tx.Save(model).Error
		}
		return tx.Create(model).Error
	})
}

// GetOrCreate 获取记录，不存在时创建默认值并返回。
// 在事务中使用 SELECT ... FOR UPDATE 锁定行。并发首插的幻影与收敛口径
// 与 Save 相同（见 runWithUniqueRetry）；单例表建议加唯一约束作为最终防线。
func (r *SingletonRepo[T]) GetOrCreate(ctx context.Context) (T, error) {
	col, err := r.primaryKeyColumn()
	if err != nil {
		var zero T
		return zero, err
	}
	var result T
	err = r.runWithUniqueRetry(ctx, col, func(tx *gorm.DB, existing T) error {
		if existing.GetID() > 0 {
			result = existing
			return nil
		}
		model := r.defaultVal()
		if cerr := tx.Create(&model).Error; cerr != nil {
			return cerr
		}
		result = model
		return nil
	})
	return result, err
}
