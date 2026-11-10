package crud

import (
	"context"
	"errors"
	"fmt"
	"testing"

	bizerrors "github.com/oekit/pkg/errors"
)

// stubEntity / stubRepo 是 BaseService 语义测试的最小仓储桩（后端无关）：
// RowsAffected 的「0 行」语义（no-op 更新）在 sqlite（计命中行）下无法自然
// 复现，必须由桩显式给出。
type stubEntity struct {
	ID   int64
	Name string
}

type stubRepo struct {
	getEntity *stubEntity
	getErr    error

	updRows int64
	updErr  error
	updOpts []UpdateOption // Update 实际收到的 opts（列白名单透传断言）

	delRows   int64
	delErr    error
	createErr error

	listItems []stubEntity // List 返回的条目
	listTotal int64        // List 返回的总数
}

func (s *stubRepo) GetByID(_ context.Context, _ int64) (*stubEntity, error) {
	if s.getErr != nil {
		return nil, s.getErr
	}
	return s.getEntity, nil
}

func (s *stubRepo) GetOne(ctx context.Context, _ *Query) (*stubEntity, error) {
	return s.GetByID(ctx, 0)
}

func (s *stubRepo) GetByCondition(ctx context.Context, _ any, _ ...any) (*stubEntity, error) {
	return s.GetByID(ctx, 0)
}

func (s *stubRepo) List(_ context.Context, _ *Query) ([]stubEntity, int64, error) {
	return s.listItems, s.listTotal, nil
}

func (s *stubRepo) Exists(_ context.Context, _ *Query) (bool, error) { return true, nil }
func (s *stubRepo) Count(_ context.Context, _ *Query) (int64, error) { return 0, nil }

func (s *stubRepo) Create(_ context.Context, _ *stubEntity) error { return s.createErr }

func (s *stubRepo) BatchCreate(_ context.Context, _ []*stubEntity, _ int) error { return nil }

func (s *stubRepo) Update(_ context.Context, _ *stubEntity, opts ...UpdateOption) (int64, error) {
	s.updOpts = opts
	return s.updRows, s.updErr
}

func (s *stubRepo) UpdateFields(_ context.Context, _ int64, _ map[string]any) (int64, error) {
	return s.updRows, s.updErr
}

func (s *stubRepo) Delete(_ context.Context, _ int64) (int64, error) { return s.delRows, s.delErr }

func (s *stubRepo) BatchDelete(_ context.Context, _ []int64) (int64, error) {
	return s.delRows, nil
}

// stubTxRepo 实现 TxRepository，覆盖 BaseService.updateInTx（事务 + 行锁读）路径。
type stubTxRepo struct {
	*stubRepo
}

func (s *stubTxRepo) RunInTx(ctx context.Context, fn func(repo Repository[stubEntity, int64]) error) error {
	return fn(s)
}

func (s *stubTxRepo) GetByIDForUpdate(ctx context.Context, id int64) (*stubEntity, error) {
	return s.GetByID(ctx, id)
}

type svcCreateReq struct{ Name string }
type svcUpdateReq struct{ Name string }

func newStubService(repo Repository[stubEntity, int64], opts ...ServiceOption) *BaseService[stubEntity, int64, svcCreateReq, svcUpdateReq] {
	return NewBaseService[stubEntity, int64, svcCreateReq, svcUpdateReq](repo, opts...).
		SetToEntity(func(r *svcCreateReq) *stubEntity { return &stubEntity{Name: r.Name} }).
		SetUpdateEntity(func(e *stubEntity, r *svcUpdateReq) { e.Name = r.Name })
}

// assertNotFound 断言默认 not-found 对外形态：BizCode=404 的 BizError，
// 且 Cause 链保留 ErrNotFound（errors.Is 可判）。
func assertNotFound(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("expected not-found error, got nil")
	}
	var be *bizerrors.BizError
	if !errors.As(err, &be) {
		t.Fatalf("expected *BizError, got %T: %v", err, err)
	}
	if be.BizCode != 404 || be.HTTPCode != 404 {
		t.Errorf("expected biz/http 404/404, got %d/%d", be.BizCode, be.HTTPCode)
	}
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("default not-found 应保留 ErrNotFound 链（errors.Is 可判），got %v", err)
	}
}

// TestBaseService_Update_FoundSemantics（#60）：ErrNotFound 只在「确实无行」
// （读步）时返回；no-op 更新（写步 0 行）默认视为成功，可配置为 not-found。
func TestBaseService_Update_FoundSemantics(t *testing.T) {
	notFound := fmt.Errorf("%w: id=9", ErrNotFound)
	ctx := context.Background()

	t.Run("行不存在 → not-found（默认 BizCode=404）", func(t *testing.T) {
		svc := newStubService(&stubRepo{getErr: notFound})
		_, err := svc.Update(ctx, 9, &svcUpdateReq{Name: "x"})
		assertNotFound(t, err)
	})

	t.Run("行存在且实际改动 → 成功", func(t *testing.T) {
		svc := newStubService(&stubRepo{getEntity: &stubEntity{ID: 9}, updRows: 1})
		got, err := svc.Update(ctx, 9, &svcUpdateReq{Name: "x"})
		if err != nil {
			t.Fatalf("Update: %v", err)
		}
		if got == nil || got.Name != "x" {
			t.Errorf("expected updated entity, got %+v", got)
		}
	})

	t.Run("no-op 更新默认口径 → 成功（不把 no-op 误报成 not-found）", func(t *testing.T) {
		svc := newStubService(&stubRepo{getEntity: &stubEntity{ID: 9}, updRows: 0})
		if _, err := svc.Update(ctx, 9, &svcUpdateReq{Name: "same"}); err != nil {
			t.Fatalf("no-op 更新默认应成功，got %v", err)
		}
	})

	t.Run("no-op 更新可配为 not-found（zeus 契约 rows==0 → 404）", func(t *testing.T) {
		svc := newStubService(&stubRepo{getEntity: &stubEntity{ID: 9}, updRows: 0}, WithNoOpUpdateAsNotFound())
		_, err := svc.Update(ctx, 9, &svcUpdateReq{Name: "same"})
		assertNotFound(t, err)
	})
}

// TestBaseService_Update_TxPath_NoOpPolicy（#60）：事务 + 行锁读路径与无事务
// 路径口径一致（读步判存在、写步 0 行按选项处理）。
func TestBaseService_Update_TxPath_NoOpPolicy(t *testing.T) {
	ctx := context.Background()

	t.Run("默认口径：no-op → 成功", func(t *testing.T) {
		stub := &stubRepo{getEntity: &stubEntity{ID: 9}, updRows: 0}
		svc := newStubService(&stubTxRepo{stub})
		if _, err := svc.Update(ctx, 9, &svcUpdateReq{Name: "same"}); err != nil {
			t.Fatalf("tx no-op 默认应成功，got %v", err)
		}
	})

	t.Run("WithNoOpUpdateAsNotFound：no-op → not-found", func(t *testing.T) {
		stub := &stubRepo{getEntity: &stubEntity{ID: 9}, updRows: 0}
		svc := newStubService(&stubTxRepo{stub}, WithNoOpUpdateAsNotFound())
		_, err := svc.Update(ctx, 9, &svcUpdateReq{Name: "same"})
		assertNotFound(t, err)
	})
}

// TestBaseService_Delete_FoundSemantics（#60）：DELETE 的 RowsAffected 无 no-op
// 歧义，0 行 = 确实无行 → not-found（负例：不再对不存在的记录静默成功）。
func TestBaseService_Delete_FoundSemantics(t *testing.T) {
	ctx := context.Background()

	t.Run("确实无行 → not-found，afterDelete 不执行", func(t *testing.T) {
		afterRan := false
		svc := newStubService(&stubRepo{delRows: 0}).
			SetAfterDelete(func(_ context.Context, _ int64) error {
				afterRan = true
				return nil
			})
		err := svc.Delete(ctx, 9)
		assertNotFound(t, err)
		if afterRan {
			t.Error("afterDelete 不应在删除 0 行时执行")
		}
	})

	t.Run("实际删除 → 成功，afterDelete 执行", func(t *testing.T) {
		afterRan := false
		svc := newStubService(&stubRepo{delRows: 1}).
			SetAfterDelete(func(_ context.Context, _ int64) error {
				afterRan = true
				return nil
			})
		if err := svc.Delete(ctx, 9); err != nil {
			t.Fatalf("Delete: %v", err)
		}
		if !afterRan {
			t.Error("afterDelete 应在实际删除后执行")
		}
	})
}

// TestBaseService_SetUpdateColumns（#61）：写入列白名单透传为
// repo.Update 的 UpdateOption{Columns}；未设置时不传 opts（全字段覆写口径）。
func TestBaseService_SetUpdateColumns(t *testing.T) {
	ctx := context.Background()

	t.Run("白名单透传 UpdateOption{Columns}", func(t *testing.T) {
		stub := &stubRepo{getEntity: &stubEntity{ID: 9}, updRows: 1}
		svc := newStubService(stub).SetUpdateColumns("name", "remark")
		if _, err := svc.Update(ctx, 9, &svcUpdateReq{Name: "x"}); err != nil {
			t.Fatalf("Update: %v", err)
		}
		if len(stub.updOpts) != 1 || len(stub.updOpts[0].Columns) != 2 || stub.updOpts[0].Columns[0] != "name" {
			t.Errorf("expected UpdateOption{Columns:[name remark]}, got %+v", stub.updOpts)
		}
	})

	t.Run("事务路径同样透传", func(t *testing.T) {
		stub := &stubRepo{getEntity: &stubEntity{ID: 9}, updRows: 1}
		svc := newStubService(&stubTxRepo{stub}).SetUpdateColumns("value")
		if _, err := svc.Update(ctx, 9, &svcUpdateReq{Name: "x"}); err != nil {
			t.Fatalf("Update: %v", err)
		}
		if len(stub.updOpts) != 1 || stub.updOpts[0].Columns[0] != "value" {
			t.Errorf("tx 路径应透传列白名单，got %+v", stub.updOpts)
		}
	})

	t.Run("未设置：不传 opts（负例，全字段覆写）", func(t *testing.T) {
		stub := &stubRepo{getEntity: &stubEntity{ID: 9}, updRows: 1}
		svc := newStubService(stub)
		if _, err := svc.Update(ctx, 9, &svcUpdateReq{Name: "x"}); err != nil {
			t.Fatalf("Update: %v", err)
		}
		if len(stub.updOpts) != 0 {
			t.Errorf("未设置白名单时不应传 UpdateOption，got %+v", stub.updOpts)
		}
	})
}

// TestBaseService_NotFoundErrMapping（#62）：not-found 对外错误可映射进消费方
// 码表/哨兵形态，内部判定路径（errors.Is(err, ErrNotFound)）不受影响。
func TestBaseService_NotFoundErrMapping(t *testing.T) {
	notFound := fmt.Errorf("%w: id=9", ErrNotFound)
	ctx := context.Background()

	t.Run("WithNotFoundErr：对外固定替换为指定错误", func(t *testing.T) {
		sentinel := errors.New("zeus: gorm.ErrRecordNotFound 形态")
		svc := newStubService(&stubRepo{getErr: notFound}, WithNotFoundErr(sentinel))
		_, err := svc.Get(ctx, 9)
		if !errors.Is(err, sentinel) {
			t.Fatalf("expected mapped sentinel, got %v", err)
		}
	})

	t.Run("WithNotFoundErr(自用码表 BizError)：body.code 直通 40401", func(t *testing.T) {
		svc := newStubService(&stubRepo{getErr: notFound},
			WithNotFoundErr(bizerrors.NewBizErrorWithStatus(40401, 404, "resource not found")))
		_, err := svc.Get(ctx, 9)
		var be *bizerrors.BizError
		if !errors.As(err, &be) || be.BizCode != 40401 {
			t.Fatalf("expected BizCode 40401, got %v", err)
		}
	})

	t.Run("WithNotFoundErr(nil)：不吞错误，维持默认 404（负例）", func(t *testing.T) {
		svc := newStubService(&stubRepo{getErr: notFound}, WithNotFoundErr(nil))
		_, err := svc.Get(ctx, 9)
		assertNotFound(t, err)
	})

	t.Run("WithErrMapper：入参为默认 BizError 404 且 ErrNotFound 链可判", func(t *testing.T) {
		var gotIn error
		sentinel := errors.New("mapped")
		svc := newStubService(&stubRepo{getErr: notFound}, WithErrMapper(func(err error) error {
			gotIn = err
			return sentinel
		}))
		_, err := svc.Get(ctx, 9)
		if !errors.Is(err, sentinel) {
			t.Fatalf("expected mapped error, got %v", err)
		}
		if !errors.Is(gotIn, ErrNotFound) {
			t.Errorf("mapper 入参应保留 ErrNotFound 链，got %v", gotIn)
		}
		var be *bizerrors.BizError
		if !errors.As(gotIn, &be) || be.BizCode != 404 {
			t.Errorf("mapper 入参应为默认 BizError 404，got %v", gotIn)
		}
	})

	t.Run("WithErrMapper 返回 nil：维持默认 404（负例，绝不吞错误）", func(t *testing.T) {
		svc := newStubService(&stubRepo{getErr: notFound}, WithErrMapper(func(error) error { return nil }))
		_, err := svc.Get(ctx, 9)
		assertNotFound(t, err)
	})

	t.Run("duplicate 等非 not-found 错误同样可映射（Create 出口）", func(t *testing.T) {
		dup := errors.New("UNIQUE constraint failed")
		svc := newStubService(&stubRepo{createErr: dup}, WithErrMapper(func(err error) error {
			if errors.Is(err, dup) {
				return bizerrors.NewBizErrorWithStatus(40901, 409, "duplicate")
			}
			return nil
		}))
		_, err := svc.Create(ctx, &svcCreateReq{Name: "x"})
		var be *bizerrors.BizError
		if !errors.As(err, &be) || be.BizCode != 40901 {
			t.Fatalf("expected BizCode 40901, got %v", err)
		}
	})
}

// TestBaseService_List_PageEnvelope_Honest（#155）：Page(0,0) 的响应信封必须
// 如实回写生效值。此前 Query.Page 原样存 NewPageRequest(0,0)，仓储层
// Offset()/Limit() 内部归一（0→1、0→20）打出 LIMIT 20，List 信封却按原始
// 0/0 回写「未分页」——调用方拿 20 条以为拿到全量。
func TestBaseService_List_PageEnvelope_Honest(t *testing.T) {
	ctx := context.Background()
	items := make([]stubEntity, 20)

	t.Run("Page(0,0) 信封按生效分页上报", func(t *testing.T) {
		svc := newStubService(&stubRepo{listItems: items, listTotal: 25})
		resp, err := svc.List(ctx, NewQuery().Page(0, 0))
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if resp.Page != 1 || resp.PageSize != 20 || resp.Pages != 2 {
			t.Errorf("信封应如实回写生效分页 page=1/pageSize=20/pages=2（报 0/0 即把 20 条当全量）, got page=%d pageSize=%d pages=%d",
				resp.Page, resp.PageSize, resp.Pages)
		}
	})

	t.Run("未调用 Page() 仍按未分页 0/0 上报（口径不变）", func(t *testing.T) {
		svc := newStubService(&stubRepo{listItems: items, listTotal: 25})
		resp, err := svc.List(ctx, NewQuery().Where("id", ">", 0))
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if resp.Page != 0 || resp.PageSize != 0 || resp.Pages != 0 {
			t.Errorf("未分页信封应为 0/0/0, got page=%d pageSize=%d pages=%d",
				resp.Page, resp.PageSize, resp.Pages)
		}
	})
}
