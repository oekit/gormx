package gormx

import (
	"errors"

	bizerrors "github.com/oekit/pkg/errors"
	"gorm.io/gorm"
)

// IsNotFound 报告 err 是否为 GORM「记录未找到」。
func IsNotFound(err error) bool {
	return errors.Is(err, gorm.ErrRecordNotFound)
}

// MapQueryError 把仓储层单条查询（Get/GetByKeyword 等）的错误映射为业务错误：
// 记录未找到 → 404 NewNotFoundError(notFoundMsg)；其余 → 500 WrapBizError(op)。
// 统一「查不到」语义，避免被笼统包成 500（历史上 group/detail、
// template/set/detail 等接口把 404 报成 500）。
func MapQueryError(op, notFoundMsg string, err error) error {
	if err == nil {
		return nil
	}
	if IsNotFound(err) {
		return bizerrors.NewNotFoundError(notFoundMsg)
	}
	return bizerrors.WrapBizError(500, op, err)
}
