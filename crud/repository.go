package crud

import "context"

// Reader 是只读仓储接口，适用于只需要查询能力的场景。
// ID 是主键类型（comparable）：int64 自增、string 文本主键（TEXT PRIMARY KEY）、
// UUID 等均可——由实体主键形态决定，接口实现与调用方共用同一类型。
type Reader[T any, ID comparable] interface {
	// GetByID 根据 ID 获取记录。
	GetByID(ctx context.Context, id ID) (*T, error)

	// GetOne 根据查询条件获取单条记录。
	GetOne(ctx context.Context, q *Query) (*T, error)

	// GetByCondition 根据原始 where 条件获取单条记录。
	GetByCondition(ctx context.Context, query any, args ...any) (*T, error)

	// List 查询列表并返回分页结果。
	List(ctx context.Context, q *Query) ([]T, int64, error)

	// Exists 检查记录是否存在。
	Exists(ctx context.Context, q *Query) (bool, error)

	// Count 统计记录数。
	Count(ctx context.Context, q *Query) (int64, error)
}

// Writer 是写入仓储接口，适用于只需要写入能力的场景。
type Writer[T any, ID comparable] interface {
	// Create 创建记录。
	Create(ctx context.Context, entity *T) error

	// BatchCreate 批量创建记录。
	BatchCreate(ctx context.Context, entities []*T, batchSize int) error

	// Update 更新记录。
	Update(ctx context.Context, entity *T) error

	// UpdateFields 根据 ID 更新指定字段。
	UpdateFields(ctx context.Context, id ID, fields map[string]any) error

	// Delete 根据 ID 删除记录。
	Delete(ctx context.Context, id ID) error

	// BatchDelete 批量删除记录。
	BatchDelete(ctx context.Context, ids []ID) error
}

// TxRepository 是支持事务的仓储可选能力。
// BaseService.Update 通过类型断言探测该接口：支持时用"事务 + FOR UPDATE
// 行锁读"消除并发 Update 的丢更新（lost update）；不支持时维持原语义。
// 事务的具体形态（数据库连接、锁语法）由实现方持有，接口层不暴露。
type TxRepository[T any, ID comparable] interface {
	Repository[T, ID]
	// RunInTx 在事务中执行 fn，fn 收到的 Repository 绑定同一事务；
	// fn 返回 error 时回滚。事务失败的写操作不落库。
	RunInTx(ctx context.Context, fn func(repo Repository[T, ID]) error) error
	// GetByIDForUpdate 按 ID 行锁读取（SELECT ... FOR UPDATE）。
	// 必须在 RunInTx 的事务内调用；不支持锁语法的后端可退化为
	// 普通读（如 sqlite 单写者模型下事务天然串行）。
	// 查询经中性 Query 构建器表达，主键列名为 "id"。
	GetByIDForUpdate(ctx context.Context, id ID) (*T, error)
}

// Repository 是通用仓储接口，组合 Reader 和 Writer。
// 不暴露底层数据库实现细节（如 *gorm.DB），保持存储后端无关。
// GORM 实现见 github.com/oekit/gormx/gormrepo（NewGormRepository）。
//
// 行级隔离契约（多租户/uid）：scope 注入是**仓储层**职责而非每个调用点
// 的纪律——由实现方在构造期挂查询装饰器（gormrepo.WithQueryDecorator），
// 所有读写路径（含按 ID 的 Get/Update/Delete）自动应用；本接口不表达
// scope 语法（后端无关），具体翻译由实现承担（GORM → Scopes）。
type Repository[T any, ID comparable] interface {
	Reader[T, ID]
	Writer[T, ID]
}
