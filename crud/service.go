package crud

import (
	"context"
	"errors"
	"fmt"

	bizerrors "github.com/oekit/pkg/errors"
	"github.com/oekit/pkg/pagination"
)

// ErrNotFound 是记录未找到的哨兵错误。
// GormRepository 将 gorm.ErrRecordNotFound 包装为此错误，
// Service 层检查此错误返回 404。
var ErrNotFound = errors.New("resource not found")

// PageResponse 分页查询结果（pkg/pagination 统一信封的别名，
// 与 data/gormx 的分页契约一致）。
type PageResponse[T any] = pagination.PageResponse[T]

// Service 是通用 Service 接口。ID 为实体主键类型（int64 自增、string 文本主键等，
// 与 Repository 的 ID 同一类型）。
type Service[T any, ID comparable, CreateReq, UpdateReq any] interface {
	// Create 创建记录。
	Create(ctx context.Context, req *CreateReq) (*T, error)

	// Get 根据 ID 获取记录。
	Get(ctx context.Context, id ID) (*T, error)

	// GetOne 根据查询条件获取单条记录。
	GetOne(ctx context.Context, q *Query) (*T, error)

	// List 查询列表（分页）。
	List(ctx context.Context, q *Query) (*PageResponse[T], error)

	// Update 更新记录。
	Update(ctx context.Context, id ID, req *UpdateReq) (*T, error)

	// Delete 删除记录。
	Delete(ctx context.Context, id ID) error
}

// BaseService 是通用 Service 基类。
// 提供基础 CRUD 逻辑，可通过 Hook 函数扩展。
type BaseService[T any, ID comparable, CreateReq, UpdateReq any] struct {
	repo Repository[T, ID]

	// Hook 函数
	beforeCreate func(ctx context.Context, req *CreateReq, entity *T) error
	afterCreate  func(ctx context.Context, entity *T) error
	beforeUpdate func(ctx context.Context, id ID, req *UpdateReq, entity *T) error
	afterUpdate  func(ctx context.Context, entity *T) error
	beforeDelete func(ctx context.Context, id ID) error
	afterDelete  func(ctx context.Context, id ID) error
	toEntity     func(req *CreateReq) *T
	updateEntity func(entity *T, req *UpdateReq)
}

// NewBaseService 创建通用 Service 实例。
// 存量 int64 主键调用点迁移：NewBaseService[T, CreateReq, UpdateReq] →
// NewBaseService[T, int64, CreateReq, UpdateReq]。
func NewBaseService[T any, ID comparable, CreateReq, UpdateReq any](repo Repository[T, ID]) *BaseService[T, ID, CreateReq, UpdateReq] {
	return &BaseService[T, ID, CreateReq, UpdateReq]{
		repo: repo,
	}
}

// SetToEntity 设置 CreateReq → Entity 的转换函数。
// 如果不设置，默认需要类型断言或手动处理。
func (s *BaseService[T, ID, C, U]) SetToEntity(fn func(req *C) *T) *BaseService[T, ID, C, U] {
	s.toEntity = fn
	return s
}

// SetUpdateEntity 设置 UpdateReq → Entity 的字段更新函数。
func (s *BaseService[T, ID, C, U]) SetUpdateEntity(fn func(entity *T, req *U)) *BaseService[T, ID, C, U] {
	s.updateEntity = fn
	return s
}

// SetBeforeCreate 设置创建前 Hook。
func (s *BaseService[T, ID, C, U]) SetBeforeCreate(fn func(ctx context.Context, req *C, entity *T) error) *BaseService[T, ID, C, U] {
	s.beforeCreate = fn
	return s
}

// SetAfterCreate 设置创建后 Hook。
func (s *BaseService[T, ID, C, U]) SetAfterCreate(fn func(ctx context.Context, entity *T) error) *BaseService[T, ID, C, U] {
	s.afterCreate = fn
	return s
}

// SetBeforeUpdate 设置更新前 Hook。
func (s *BaseService[T, ID, C, U]) SetBeforeUpdate(fn func(ctx context.Context, id ID, req *U, entity *T) error) *BaseService[T, ID, C, U] {
	s.beforeUpdate = fn
	return s
}

// SetAfterUpdate 设置更新后 Hook。
func (s *BaseService[T, ID, C, U]) SetAfterUpdate(fn func(ctx context.Context, entity *T) error) *BaseService[T, ID, C, U] {
	s.afterUpdate = fn
	return s
}

// SetBeforeDelete 设置删除前 Hook。
func (s *BaseService[T, ID, C, U]) SetBeforeDelete(fn func(ctx context.Context, id ID) error) *BaseService[T, ID, C, U] {
	s.beforeDelete = fn
	return s
}

// SetAfterDelete 设置删除后 Hook。
func (s *BaseService[T, ID, C, U]) SetAfterDelete(fn func(ctx context.Context, id ID) error) *BaseService[T, ID, C, U] {
	s.afterDelete = fn
	return s
}

// Create 创建记录。
// 必须通过 SetToEntity 设置转换函数，否则返回错误。
func (s *BaseService[T, ID, C, U]) Create(ctx context.Context, req *C) (*T, error) {
	if s.toEntity == nil {
		return nil, fmt.Errorf("crud: toEntity converter not set; call SetToEntity before Create")
	}
	entity := s.toEntity(req)

	if s.beforeCreate != nil {
		if err := s.beforeCreate(ctx, req, entity); err != nil {
			return nil, err
		}
	}

	if err := s.repo.Create(ctx, entity); err != nil {
		return nil, err
	}

	if s.afterCreate != nil {
		if err := s.afterCreate(ctx, entity); err != nil {
			return nil, err
		}
	}

	return entity, nil
}

// Get 根据 ID 获取记录。
func (s *BaseService[T, ID, C, U]) Get(ctx context.Context, id ID) (*T, error) {
	entity, err := s.repo.GetByID(ctx, id)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, bizerrors.WrapBizErrorWithStatus(404, 404, "resource not found", err)
		}
		return nil, err
	}
	return entity, nil
}

// GetOne 根据查询条件获取单条记录。
func (s *BaseService[T, ID, C, U]) GetOne(ctx context.Context, q *Query) (*T, error) {
	entity, err := s.repo.GetOne(ctx, q)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, bizerrors.WrapBizErrorWithStatus(404, 404, "resource not found", err)
		}
		return nil, err
	}
	return entity, nil
}

// List 查询列表（分页）。
// 分页字段为诚实语义：当 q 为 nil 或未调用 Page() 时，查询为全量
// （未分页），返回的 Page/PageSize 为 0 —— 不假装默认 Page=1/PageSize=20
// 误导调用方把全量数据当有界结果处理。
func (s *BaseService[T, ID, C, U]) List(ctx context.Context, q *Query) (*PageResponse[T], error) {
	list, total, err := s.repo.List(ctx, q)
	if err != nil {
		return nil, err
	}

	// 未分页（q 为 nil 或未设置 Page）时 Page/PageSize = 0，表示"未分页"。
	page, pageSize := 0, 0
	if q != nil && q.GetPage() != nil {
		page = q.GetPage().Page
		pageSize = q.GetPage().PageSize
	}

	// Pages 与 gormx.Paginate / pkg/pagination 口径一致:未分页时为 0,
	// 分页时 ceil(total/pageSize)(2026-09-06 复审修复:原实现漏填,
	// 经 crudh 暴露的列表 API pages 字段恒为 0)
	var pages int
	if pageSize > 0 {
		pages = int((total + int64(pageSize) - 1) / int64(pageSize))
	}

	return &PageResponse[T]{
		Items:    list,
		Total:    total,
		Page:     page,
		PageSize: pageSize,
		Pages:    pages,
	}, nil
}

// Update 更新记录。
// 必须通过 SetUpdateEntity 设置字段更新函数：未设置时返回错误，
// 不再静默执行一次无变更的 Save 并"成功"（与 Create 的 SetToEntity
// 约束对称）。
//
// 并发语义：repo 实现 [TxRepository] 时走"事务 + FOR UPDATE
// 行锁读"，消除并发 Update 同一 id 的丢更新（lost update）；afterUpdate
// 钩子也随之入事务——钩子失败整体回滚，不再出现"钩子报错但写已提交"。
// 仓储不支持事务能力时回退为无锁读-改-写（原语义）。
func (s *BaseService[T, ID, C, U]) Update(ctx context.Context, id ID, req *U) (*T, error) {
	if txRepo, ok := s.repo.(TxRepository[T, ID]); ok {
		return s.updateInTx(ctx, txRepo, id, req)
	}
	return s.updateNoTx(ctx, id, req)
}

// updateInTx 事务路径：行锁读 → 业务转换 → 写入，原子提交。
func (s *BaseService[T, ID, C, U]) updateInTx(ctx context.Context, txRepo TxRepository[T, ID], id ID, req *U) (*T, error) {
	var result *T
	err := txRepo.RunInTx(ctx, func(repo Repository[T, ID]) error {
		lockRepo, ok := repo.(TxRepository[T, ID])
		if !ok {
			return fmt.Errorf("crud: tx repo does not implement TxRepository")
		}
		entity, err := lockRepo.GetByIDForUpdate(ctx, id)
		if err != nil {
			return err
		}

		if s.beforeUpdate != nil {
			if err := s.beforeUpdate(ctx, id, req, entity); err != nil {
				return err
			}
		}

		// 检查顺序与无事务路径一致（GetByID → beforeUpdate → updateEntity）
		if s.updateEntity == nil {
			return fmt.Errorf("crud: updateEntity converter not set; call SetUpdateEntity before Update")
		}
		s.updateEntity(entity, req)

		if err := repo.Update(ctx, entity); err != nil {
			return err
		}

		if s.afterUpdate != nil {
			if err := s.afterUpdate(ctx, entity); err != nil {
				return err // 入事务：钩子失败整体回滚
			}
		}
		result = entity
		return nil
	})
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, bizerrors.WrapBizErrorWithStatus(404, 404, "resource not found", err)
		}
		return nil, err
	}
	return result, nil
}

// updateNoTx 无事务回退路径（仓储不支持 TxRepository 时的原语义）。
func (s *BaseService[T, ID, C, U]) updateNoTx(ctx context.Context, id ID, req *U) (*T, error) {
	entity, err := s.repo.GetByID(ctx, id)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, bizerrors.WrapBizErrorWithStatus(404, 404, "resource not found", err)
		}
		return nil, err
	}

	if s.beforeUpdate != nil {
		if err := s.beforeUpdate(ctx, id, req, entity); err != nil {
			return nil, err
		}
	}

	// 回归修复：忘 SetUpdateEntity 时持久化层只会得到一次无变更的 Save，
	// 必须显式报错而不是假成功。
	if s.updateEntity == nil {
		return nil, fmt.Errorf("crud: updateEntity converter not set; call SetUpdateEntity before Update")
	}
	s.updateEntity(entity, req)

	if err := s.repo.Update(ctx, entity); err != nil {
		return nil, err
	}

	if s.afterUpdate != nil {
		if err := s.afterUpdate(ctx, entity); err != nil {
			return nil, err
		}
	}

	return entity, nil
}

// Delete 删除记录。
func (s *BaseService[T, ID, C, U]) Delete(ctx context.Context, id ID) error {
	if s.beforeDelete != nil {
		if err := s.beforeDelete(ctx, id); err != nil {
			return err
		}
	}

	if err := s.repo.Delete(ctx, id); err != nil {
		return err
	}

	if s.afterDelete != nil {
		if err := s.afterDelete(ctx, id); err != nil {
			return err
		}
	}

	return nil
}
