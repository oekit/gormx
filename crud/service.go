package crud

import (
	"context"
	"errors"
	"fmt"

	bizerrors "github.com/oekit/pkg/errors"
	"github.com/oekit/pkg/pagination"
)

// ErrNotFound 是记录未找到的哨兵错误。
// GormRepository 将 gorm.ErrRecordNotFound 包装为此错误。
//
// 判定与出口分离：Service 内部判定一律基于 errors.Is(err, ErrNotFound)
// （不受错误映射影响）；对外形态默认包装为 BizCode=404 的 BizError
// （Cause 链保留本哨兵，errors.Is 仍可判），可经 WithErrMapper /
// WithNotFoundErr 替换为消费方码表/哨兵形态（如 40401、gorm.ErrRecordNotFound）。
var ErrNotFound = errors.New("resource not found")

// ErrMapperFunc 是 Service 对外错误的映射钩子（同 middleware.SetStatusMapper 哲学）。
// 入参 err 为 crud 按默认规则处理后即将返回的错误：not-found 场景是
// BizCode=404 的 BizError（errors.Is(err, ErrNotFound) 成立），duplicate 等
// 其余错误原样传入。返回非 nil 即以返回值替换对外错误（含哨兵 error 形态）；
// 返回 nil 维持默认——绝不吞错误。
type ErrMapperFunc func(err error) error

// serviceConfig 是 BaseService 的构造配置（NewBaseService 的 ServiceOption 目标）。
type serviceConfig struct {
	// mapErr 对外错误映射钩子；nil = 默认（not-found → BizCode 404）。
	mapErr ErrMapperFunc
	// noOpUpdateAsNotFound：Update 命中 0 行（no-op 更新）按 not-found 处理。
	noOpUpdateAsNotFound bool
}

// ServiceOption 配置 BaseService（命名对齐仓内硬规则，避免裸 Option）。
type ServiceOption func(*serviceConfig)

// WithNotFoundErr 把 **not-found** 对外错误固定替换为 err（消费方哨兵或
// 自有码表 BizError，如 bizerrors.NewBizErrorWithStatus(40401, 404, "...")）。
// 只替换 not-found（errors.Is(err, ErrNotFound) 命中）：duplicate 等其余
// 错误**原样上抛**——旧实现无条件替换全部错误（mapErr 恒返回 err），唯一键
// 冲突（duplicate/409 语义）被伪装成 not-found 404，gormx.IsUniqueViolation
// 判据随之恒 false（#160）。duplicate 等非 not-found 错误的码表映射请用
// WithErrMapper（契约见 crud/doc.go）。
// err 为 nil 时视为不替换（维持默认 BizCode 404，绝不把 not-found 吞成 nil）。
// 与 WithErrMapper 共用同一槽位，后设置者生效。
func WithNotFoundErr(err error) ServiceOption {
	return func(c *serviceConfig) {
		c.mapErr = func(in error) error {
			if errors.Is(in, ErrNotFound) {
				return err
			}
			return in
		}
	}
}

// WithErrMapper 注入错误映射钩子：把 not-found / duplicate 等对外错误映射进
// 消费方自有码表（含哨兵 error 形态），不改 crud 内部判定路径（内部仍按
// errors.Is(err, ErrNotFound) 判 not-found）。
// 与 WithNotFoundErr 共用同一槽位，后设置者生效。
func WithErrMapper(fn ErrMapperFunc) ServiceOption {
	return func(c *serviceConfig) { c.mapErr = fn }
}

// WithNoOpUpdateAsNotFound 把 Update 的 0 行更新按 not-found 处理
// （zeus 契约「rows==0 → 404」）。0 行更新 = no-op（读步已确认行存在，写步
// 未命中/未改变，MySQL 同值更新计 0 行）。
// 默认（未设置）0 行更新视为成功——「行不存在」只在读步 ErrNotFound
// 判定处返回，不把 no-op 误报成 not-found。
func WithNoOpUpdateAsNotFound() ServiceOption {
	return func(c *serviceConfig) { c.noOpUpdateAsNotFound = true }
}

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
	cfg  serviceConfig

	// updateColumns 是 Update 写入的列白名单（SetUpdateColumns）；
	// 空 = 全字段覆写。
	updateColumns []string

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
// opts 配置 not-found 错误映射（WithNotFoundErr / WithErrMapper）与
// no-op 更新口径（WithNoOpUpdateAsNotFound）。
// 存量 int64 主键调用点迁移：NewBaseService[T, CreateReq, UpdateReq] →
// NewBaseService[T, int64, CreateReq, UpdateReq]。
func NewBaseService[T any, ID comparable, CreateReq, UpdateReq any](repo Repository[T, ID], opts ...ServiceOption) *BaseService[T, ID, CreateReq, UpdateReq] {
	cfg := serviceConfig{}
	for _, o := range opts {
		o(&cfg)
	}
	return &BaseService[T, ID, CreateReq, UpdateReq]{
		repo: repo,
		cfg:  cfg,
	}
}

// outErr 是全部对外错误出口的统一收口：经 ErrMapperFunc 可替换为消费方
// 码表/哨兵形态。mapErr 返回 nil 时维持默认错误（绝不吞错误）。
func (s *BaseService[T, ID, C, U]) outErr(err error) error {
	if err == nil {
		return nil
	}
	if s.cfg.mapErr != nil {
		if mapped := s.cfg.mapErr(err); mapped != nil {
			return mapped
		}
	}
	return err
}

// notFoundError 把内部 not-found 哨兵包装为对外错误（默认 BizCode=404 的
// BizError，Cause 链保留 ErrNotFound，errors.Is 可判），再经 outErr 映射。
func (s *BaseService[T, ID, C, U]) notFoundError(err error) error {
	return s.outErr(bizerrors.WrapBizErrorWithStatus(404, 404, "resource not found", err))
}

// updateWriteOptions 把服务层的写入列白名单透传为 repo.Update 的 UpdateOption。
func (s *BaseService[T, ID, C, U]) updateWriteOptions() []UpdateOption {
	if len(s.updateColumns) == 0 {
		return nil
	}
	return []UpdateOption{{Columns: s.updateColumns}}
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

// SetUpdateColumns 设置 Update 写入的列白名单（immutable 列保护）。
// 非空时 Update 只写白名单内的列（含零值列），白名单外的列绝不落库——
// updateEntity 对 immutable 列（如字典 type）的修改在写入时被忽略；
// 空（未设置，缺省）维持全字段覆写。透传 repo.Update 的 UpdateOption{Columns}。
func (s *BaseService[T, ID, C, U]) SetUpdateColumns(cols ...string) *BaseService[T, ID, C, U] {
	s.updateColumns = cols
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
		return nil, s.outErr(fmt.Errorf("crud: toEntity converter not set; call SetToEntity before Create"))
	}
	entity := s.toEntity(req)

	if s.beforeCreate != nil {
		if err := s.beforeCreate(ctx, req, entity); err != nil {
			return nil, s.outErr(err)
		}
	}

	if err := s.repo.Create(ctx, entity); err != nil {
		return nil, s.outErr(err)
	}

	if s.afterCreate != nil {
		if err := s.afterCreate(ctx, entity); err != nil {
			return nil, s.outErr(err)
		}
	}

	return entity, nil
}

// Get 根据 ID 获取记录。
// 记录不存在 → not-found 对外错误（默认 BizCode=404，可经 WithErrMapper /
// WithNotFoundErr 映射进消费方码表）。
func (s *BaseService[T, ID, C, U]) Get(ctx context.Context, id ID) (*T, error) {
	entity, err := s.repo.GetByID(ctx, id)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, s.notFoundError(err)
		}
		return nil, s.outErr(err)
	}
	return entity, nil
}

// GetOne 根据查询条件获取单条记录。
// 记录不存在 → not-found 对外错误（口径同 Get）。
func (s *BaseService[T, ID, C, U]) GetOne(ctx context.Context, q *Query) (*T, error) {
	entity, err := s.repo.GetOne(ctx, q)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, s.notFoundError(err)
		}
		return nil, s.outErr(err)
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
		return nil, s.outErr(err)
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
// found 语义：ErrNotFound 只在读步「确实无行」时返回；写步命中 0 行（no-op）
// 按 WithNoOpUpdateAsNotFound 口径处理（默认视为成功）。
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
			if hookErr := s.beforeUpdate(ctx, id, req, entity); hookErr != nil {
				return hookErr
			}
		}

		// 检查顺序与无事务路径一致（GetByID → beforeUpdate → updateEntity）
		if s.updateEntity == nil {
			return fmt.Errorf("crud: updateEntity converter not set; call SetUpdateEntity before Update")
		}
		s.updateEntity(entity, req)

		rows, err := repo.Update(ctx, entity, s.updateWriteOptions()...)
		if err != nil {
			return err
		}
		if rows == 0 && s.cfg.noOpUpdateAsNotFound {
			// no-op 口径：0 行 → not-found（入事务，整体回滚）
			return fmt.Errorf("%w: id=%v", ErrNotFound, id)
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
			return nil, s.notFoundError(err)
		}
		return nil, s.outErr(err)
	}
	return result, nil
}

// updateNoTx 无事务回退路径（仓储不支持 TxRepository 时的原语义）。
// found 语义：ErrNotFound 只在读步「确实无行」时返回；写步命中 0 行（no-op）
// 按 WithNoOpUpdateAsNotFound 口径处理（默认视为成功）。
func (s *BaseService[T, ID, C, U]) updateNoTx(ctx context.Context, id ID, req *U) (*T, error) {
	entity, err := s.repo.GetByID(ctx, id)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, s.notFoundError(err)
		}
		return nil, s.outErr(err)
	}

	if s.beforeUpdate != nil {
		if hookErr := s.beforeUpdate(ctx, id, req, entity); hookErr != nil {
			return nil, s.outErr(hookErr)
		}
	}

	// 回归修复：忘 SetUpdateEntity 时持久化层只会得到一次无变更的 Save，
	// 必须显式报错而不是假成功。
	if s.updateEntity == nil {
		return nil, s.outErr(fmt.Errorf("crud: updateEntity converter not set; call SetUpdateEntity before Update"))
	}
	s.updateEntity(entity, req)

	rows, err := s.repo.Update(ctx, entity, s.updateWriteOptions()...)
	if err != nil {
		return nil, s.outErr(err)
	}
	if rows == 0 && s.cfg.noOpUpdateAsNotFound {
		// no-op 口径：0 行 → not-found（默认口径下视为成功，不误报）
		return nil, s.notFoundError(fmt.Errorf("%w: id=%v", ErrNotFound, id))
	}

	if s.afterUpdate != nil {
		if err := s.afterUpdate(ctx, entity); err != nil {
			return nil, s.outErr(err)
		}
	}

	return entity, nil
}

// Delete 删除记录。
// found 语义：DELETE 的 RowsAffected 无 no-op 歧义（0 行 = 确实无行），
// 记录不存在时返回 not-found 对外错误（默认 BizCode=404，可经 WithErrMapper /
// WithNotFoundErr 映射），不再对不存在的记录静默成功；afterDelete 只在
// 实际删除后执行。
func (s *BaseService[T, ID, C, U]) Delete(ctx context.Context, id ID) error {
	if s.beforeDelete != nil {
		if err := s.beforeDelete(ctx, id); err != nil {
			return s.outErr(err)
		}
	}

	rows, err := s.repo.Delete(ctx, id)
	if err != nil {
		return s.outErr(err)
	}
	if rows == 0 {
		return s.notFoundError(fmt.Errorf("%w: id=%v", ErrNotFound, id))
	}

	if s.afterDelete != nil {
		if err := s.afterDelete(ctx, id); err != nil {
			return s.outErr(err)
		}
	}

	return nil
}
