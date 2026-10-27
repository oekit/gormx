package gormx

import (
	"testing"

	"github.com/oekit/pkg/pagination"
	"gorm.io/gorm"
)

func TestIsUniqueViolation_Nil(t *testing.T) {
	if IsUniqueViolation(nil) {
		t.Error("expected false for nil")
	}
}

func TestIsUniqueViolation_DuplicatedKey(t *testing.T) {
	if !IsUniqueViolation(gorm.ErrDuplicatedKey) {
		t.Error("expected true for ErrDuplicatedKey")
	}
}

func TestIsUniqueViolation_MySQL(t *testing.T) {
	err := &mockErr{msg: "Duplicate entry 'abc' for key 'idx_name'"}
	if !IsUniqueViolation(err) {
		t.Error("expected true for MySQL duplicate entry")
	}
}

func TestIsUniqueViolation_SQLite(t *testing.T) {
	// glebarez/sqlite（纯 Go 驱动）的唯一冲突文案；既不映射 gorm.ErrDuplicatedKey
	// （未开 TranslateError），也不含 mysql/pg 的措辞（oekit/gormx#30）。
	err := &mockErr{msg: "UNIQUE constraint failed: file_codes.pickup_code"}
	if !IsUniqueViolation(err) {
		t.Error("expected true for SQLite UNIQUE constraint failed")
	}
}

func TestIsUniqueViolation_NotAUniqueViolation(t *testing.T) {
	// 负例：普通约束错误**不得**被宽判据误判成唯一冲突。
	for _, msg := range []string{
		"CHECK constraint failed: size > 0",
		"FOREIGN KEY constraint failed",
		"database is locked",
	} {
		if IsUniqueViolation(&mockErr{msg: msg}) {
			t.Errorf("expected false for %q", msg)
		}
	}
}

func TestIsUniqueViolation_PostgreSQL(t *testing.T) {
	err := &mockErr{msg: "duplicate key value violates unique constraint"}
	if !IsUniqueViolation(err) {
		t.Error("expected true for PostgreSQL duplicate key")
	}
}

func TestIsUniqueViolation_PostgreSQL_Code(t *testing.T) {
	err := &mockErr{msg: "ERROR: duplicate key value (23505)"}
	if !IsUniqueViolation(err) {
		t.Error("expected true for PostgreSQL 23505")
	}
}

func TestIsUniqueViolation_OtherError(t *testing.T) {
	err := &mockErr{msg: "connection refused"}
	if IsUniqueViolation(err) {
		t.Error("expected false for other error")
	}
}

func TestPageRequest_Fields(t *testing.T) {
	p := pagination.PageRequest{Page: 2, PageSize: 20}
	if p.Page != 2 {
		t.Errorf("expected page 2, got %d", p.Page)
	}
	if p.PageSize != 20 {
		t.Errorf("expected pageSize 20, got %d", p.PageSize)
	}
}

func TestPageRequest_Normalize(t *testing.T) {
	p := pagination.PageRequest{Page: 0, PageSize: 0}
	p.Normalize()
	if p.Page != pagination.DefaultPage {
		t.Errorf("expected default page %d, got %d", pagination.DefaultPage, p.Page)
	}
	if p.PageSize != pagination.DefaultPageSize {
		t.Errorf("expected default pageSize %d, got %d", pagination.DefaultPageSize, p.PageSize)
	}
}

func TestPageRequest_Normalize_MaxPageSize(t *testing.T) {
	p := pagination.PageRequest{Page: 1, PageSize: 1000}
	p.Normalize()
	if p.PageSize != pagination.MaxPageSize {
		t.Errorf("expected max pageSize %d, got %d", pagination.MaxPageSize, p.PageSize)
	}
}

func TestPageRequest_OffsetLimit(t *testing.T) {
	p := pagination.PageRequest{Page: 3, PageSize: 10}
	if p.Offset() != 20 {
		t.Errorf("expected offset 20, got %d", p.Offset())
	}
	if p.Limit() != 10 {
		t.Errorf("expected limit 10, got %d", p.Limit())
	}
}

func TestPageResponse_Fields(t *testing.T) {
	p := PageResponse[string]{
		Items:    []string{"a", "b"},
		Total:    100,
		Page:     1,
		PageSize: 20,
		Pages:    5,
	}
	if p.Total != 100 {
		t.Errorf("expected total 100, got %d", p.Total)
	}
	if p.Pages != 5 {
		t.Errorf("expected pages 5, got %d", p.Pages)
	}
	if len(p.Items) != 2 {
		t.Errorf("expected 2 items, got %d", len(p.Items))
	}
}

func TestNewPageRequest(t *testing.T) {
	p := pagination.NewPageRequest(2, 50)
	if p.Page != 2 || p.PageSize != 50 {
		t.Errorf("expected {2 50}, got %+v", p)
	}
}

func TestPaginateScope(t *testing.T) {
	req := pagination.NewPageRequest(1, 20)
	scope := PaginateScope(req)
	if scope == nil {
		t.Error("expected non-nil scope")
	}
}

type mockErr struct{ msg string }

func (e *mockErr) Error() string { return e.msg }
