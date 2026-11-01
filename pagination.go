package gormx

import (
	"math"

	"github.com/oekit/pkg/pagination"
	"gorm.io/gorm"
)

// PageResponse 分页查询的响应结果（pkg/pagination 统一信封的别名）。
// 字段为 Items（JSON "items"），与 crud、hertz/crudh 的契约统一为
// pkg/pagination.PageResponse。
type PageResponse[T any] = pagination.PageResponse[T]

// Paginate 执行分页查询，返回指定类型的分页结果。
// queryFn 只调用一次（GORM 的 *gorm.DB 是不可变的，每次方法调用返回新实例）。
// queryFn 可传 nil（等价于恒等 scope）。
//
// 若调用方既未在 db 上 Model() 也未在 queryFn 里 Table()/Model()，
// 自动用泛型参数 T 补 Model(new(T))——否则 Count 步会报
// "Table not set"，这是最常见的用法遗漏（13+ 处调用踩过）。
// 显式 Table() 的自定义行扫描（join/Select 到非表结构体）不受影响。
func Paginate[T any](db *gorm.DB, req *pagination.PageRequest, queryFn func(db *gorm.DB) *gorm.DB) (*PageResponse[T], error) {
	// nil 请求按默认页兜底（handler 忘绑定分页参数时传 nil 是常见笔误，
	// 直接触发 Normalize 的 nil dereference panic）
	if req == nil {
		req = pagination.NewPageRequest(pagination.DefaultPage, pagination.DefaultPageSize)
	}
	req.Normalize()

	baseDB := db
	if queryFn != nil {
		baseDB = queryFn(db)
	}
	if st := baseDB.Statement; st != nil && st.Model == nil && st.Table == "" {
		baseDB = baseDB.Model(new(T))
	}

	var total int64
	if err := baseDB.Count(&total).Error; err != nil {
		return nil, err
	}

	var items []T
	if err := baseDB.Offset(req.Offset()).Limit(req.Limit()).Find(&items).Error; err != nil {
		return nil, err
	}

	pages := 0
	if req.PageSize > 0 && total > 0 {
		pages = int(math.Ceil(float64(total) / float64(req.PageSize)))
	}

	return &PageResponse[T]{
		Items:    items,
		Total:    total,
		Page:     req.Page,
		PageSize: req.PageSize,
		Pages:    pages,
	}, nil
}

// PaginateScope 返回一个 GORM Scope 函数，自动应用分页的 Offset 和 Limit。
func PaginateScope(req *pagination.PageRequest) func(db *gorm.DB) *gorm.DB {
	return func(db *gorm.DB) *gorm.DB {
		return db.Offset(req.Offset()).Limit(req.Limit())
	}
}
