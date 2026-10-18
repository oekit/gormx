// Package crud 提供通用 CRUD 的泛型数据层基础设施。
//
// 包含查询构建器、仓储层和服务层，大幅减少样板代码。
// 本包存储后端无关（不依赖任何 ORM SDK）；GORM 实现见兄弟包
// github.com/oekit/gormx/gormrepo（NewGormRepository）。HTTP Handler 见
// github.com/oekit/hertz/crudh。
//
// # 查询条件的 fail-closed 契约
//
// Query.Where/Or 的操作符走白名单（=, !=, <>, <, >, <=, >=, LIKE, ILIKE,
// IN, NOT IN, BETWEEN, NOT BETWEEN, IS NULL, IS NOT NULL），值始终参数化。
// 未知操作符或非法的 BETWEEN/NOT BETWEEN 值形状（支持 [2]any、len==2 的
// []any、两个连续变参三种形状）一律渲染为恒假的 1=0 —— 过滤条件绝不会被
// 静默丢弃，避免租户/权限过滤凭空消失造成数据暴露。
//
// # 用法
//
//	// 定义实体（嵌入 gormx.Model）
//	type Project struct {
//	    gormx.Model
//	    Name   string `json:"name"`
//	    Status int    `json:"status"`
//	}
//
//	// 定义请求
//	type CreateProjectReq struct {
//	    Name   string `json:"name" validate:"required"`
//	    Status int    `json:"status"`
//	}
//
//	// 使用（GORM 后端；ID 为实体主键类型——int64 自增或 string 文本主键等）
//	repo := gormrepo.NewGormRepository[Project, int64](db)
//	svc := crud.NewBaseService[Project, int64, CreateProjectReq, UpdateProjectReq](repo)
//	// HTTP Handler 见 github.com/oekit/hertz/crudh
//	handler := crudh.NewHandler(svc)
//	handler.RegisterRoutes(group, "/projects")
//
// # 自定义存储后端
//
// 实现 Repository[T, ID] 接口（可选实现 TxRepository[T, ID] 解锁 BaseService.Update
// 的"事务 + 行锁读"并发语义），Query 经 Spec() 物化为后端无关的 QuerySpec
// （AND/OR 条件、排序、预加载、字段裁剪、分组、分页、悲观锁语义位），
// 由实现方翻译为具体查询语句——参考 gormrepo 的 applySpec。
package crud
