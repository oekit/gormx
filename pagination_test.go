package gormx

import (
	"fmt"
	"sync"
	"testing"

	"github.com/oekit/pkg/pagination"
	"gorm.io/gorm"
)

// ---- #101：Paginate 不得原地 Normalize 调用方的 PageRequest ----

// TestPaginate_DoesNotMutateCallerRequest 回归（#101）：归一化必须在内部
// 副本上计算。此前 req.Normalize() 原地改写调用方结构体——handler 侧共享的
// 包级默认 PageRequest 指针传入时，并发请求就是数据竞争。
func TestPaginate_DoesNotMutateCallerRequest(t *testing.T) {
	db := newTestDB(t, &TestBase{})
	if err := db.Create(&TestBase{Name: "x"}).Error; err != nil {
		t.Fatal(err)
	}

	req := &pagination.PageRequest{Page: 0, PageSize: 0} // 非法值，应被内部兜底
	resp, err := Paginate[TestBase](db, req, nil)
	if err != nil {
		t.Fatal(err)
	}
	if req.Page != 0 || req.PageSize != 0 {
		t.Errorf("调用方 req 被原地改写: got %+v, want {0 0}", req)
	}
	// 响应回显的是归一化后的值（与旧行为一致）
	if resp.Page != pagination.DefaultPage || resp.PageSize != pagination.DefaultPageSize {
		t.Errorf("resp 回显应归一化: got page=%d pageSize=%d", resp.Page, resp.PageSize)
	}
}

// TestPaginate_SharedRequestConcurrentRace 在 -race 下验证共享 req 并发分页
// 无写竞争（修复前 Normalize 原地写 + 并发读必被 race detector 捕获）。
func TestPaginate_SharedRequestConcurrentRace(t *testing.T) {
	db := newTestDB(t, &TestBase{})
	for i := 0; i < 5; i++ {
		if err := db.Create(&TestBase{Name: "row"}).Error; err != nil {
			t.Fatal(err)
		}
	}
	// 模拟 handler 侧的包级共享默认值指针
	shared := &pagination.PageRequest{}

	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp, err := Paginate[TestBase](db, shared, nil)
			if err == nil && resp.Total != 5 {
				err = fmt.Errorf("total = %d, want 5", resp.Total)
			}
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
}

// ---- #95：无 ORDER BY 的分页注入主键 tiebreaker ----

// TestPaginate_InjectsPrimaryKeyTiebreak 回归（#95）：不挂 Order 直接分页时
// 自动注入主键 DESC——OFFSET/LIMIT 无序即未定义，跨页边界行可能重复或丢失。
// 注入后分页是全序确定的：页 1 恰为 id 最大的 N 条、页间无重叠无遗漏。
func TestPaginate_InjectsPrimaryKeyTiebreak(t *testing.T) {
	db := newTestDB(t, &TestBase{})
	for i := 0; i < 15; i++ {
		if err := db.Create(&TestBase{Name: "same"}).Error; err != nil {
			t.Fatal(err)
		}
	}

	p1, err := Paginate[TestBase](db, pagination.NewPageRequest(1, 10), nil)
	if err != nil {
		t.Fatal(err)
	}
	p2, err := Paginate[TestBase](db, pagination.NewPageRequest(2, 10), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(p1.Items) != 10 || len(p2.Items) != 5 {
		t.Fatalf("page sizes: p1=%d p2=%d, want 10/5", len(p1.Items), len(p2.Items))
	}

	// 页 1 = id 前 10 大（DESC），页 2 = 剩余 5 条，页间不重不漏
	seen := map[int64]bool{}
	for i, it := range p1.Items {
		want := int64(15 - i)
		if it.ID != want {
			t.Errorf("p1[%d].ID = %d, want %d（主键 DESC tiebreaker 未生效）", i, it.ID, want)
		}
		seen[it.ID] = true
	}
	for _, it := range p2.Items {
		if seen[it.ID] {
			t.Errorf("行 %d 跨页重复（无序分页的典型症状）", it.ID)
		}
		seen[it.ID] = true
	}
	if len(seen) != 15 {
		t.Errorf("两页覆盖 %d 行, want 15（有行丢失）", len(seen))
	}
}

// TestPaginate_RespectsExplicitOrder 调用方显式给了 ORDER BY 时不注入
// tiebreaker，尊重调用方口径。
func TestPaginate_RespectsExplicitOrder(t *testing.T) {
	db := newTestDB(t, &TestBase{})
	for i := 0; i < 12; i++ {
		if err := db.Create(&TestBase{Name: "row"}).Error; err != nil {
			t.Fatal(err)
		}
	}

	resp, err := Paginate[TestBase](db, pagination.NewPageRequest(1, 10), func(db *gorm.DB) *gorm.DB {
		return db.Order("id ASC")
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Items) != 10 {
		t.Fatalf("got %d items, want 10", len(resp.Items))
	}
	if resp.Items[0].ID != 1 || resp.Items[9].ID != 10 {
		t.Errorf("显式 id ASC 应被保留: got [%d..%d], want [1..10]", resp.Items[0].ID, resp.Items[9].ID)
	}
}

// TestPaginate_HasOrderByDetection hasOrderBy 检测的单元面：字符串 Order、
// clause 形态 Order、未挂 Order 三种形态。
func TestPaginate_HasOrderByDetection(t *testing.T) {
	db := newTestDB(t, &TestBase{})

	if hasOrderBy(db.Model(&TestBase{}).Where("name = ?", "x")) {
		t.Error("未挂 Order 的语句不应检出 ORDER BY")
	}
	if !hasOrderBy(db.Model(&TestBase{}).Order("name")) {
		t.Error("字符串 Order 应检出 ORDER BY")
	}
	// Order("") 在 gorm 中是 no-op（不添加子句），应视为未排序
	if hasOrderBy(db.Model(&TestBase{}).Order("")) {
		t.Error(`Order("") 是 no-op，不应检出 ORDER BY`)
	}
}

// TestPaginate_TiebreakNoSchemaSkipped 显式 Table 的自定义扫描形态没有
// 主键可依：不注入、不报错（文档契约：该形态由调用方自带稳定排序）。
func TestPaginate_TiebreakNoSchemaSkipped(t *testing.T) {
	db := newTestDB(t, &TestBase{})
	if err := db.Create(&TestBase{Name: "explicit"}).Error; err != nil {
		t.Fatal(err)
	}
	resp, err := Paginate[flatRow](db, pagination.NewPageRequest(1, 10), func(db *gorm.DB) *gorm.DB {
		return db.Table("test_bases").Select("id, name").Where("name = ?", "explicit")
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Items) != 1 || resp.Items[0].Name != "explicit" {
		t.Errorf("explicit-table form broken: %+v", resp.Items)
	}
}

// ---- #136：Count 未丢弃 Selects ⇒ COUNT(<列>) 漏计 NULL 行 ----

// nullableRow 是 #136 回归模型：Name 可空——COUNT(name) 不计 NULL 行，
// count(*) 才是「行数」口径。
type nullableRow struct {
	BaseModel
	Name *string
}

// TestPaginate_CountDropsSelects 回归（#136）：带 Select("name") 的分页，
// Count 不得作用在选中列上——GORM 在 Statement.Selects 非空时生成
// COUNT(<列>) 而非 count(*)，目标列为 NULL 的行不计入 total ⇒ 同一响应里
// Total=2 而 len(Items)=3 自相矛盾、Pages 偏一（第 2 页永远取不到数据）。
// 修复后计数语句丢弃 Selects（与 gormrepo.countStatement 同一口径），
// find 路径的投影不受影响。
func TestPaginate_CountDropsSelects(t *testing.T) {
	db := newTestDB(t, &nullableRow{})
	a, c := "a", "c"
	for _, r := range []*nullableRow{{Name: &a}, {}, {Name: &c}} {
		if err := db.Create(r).Error; err != nil {
			t.Fatal(err)
		}
	}

	resp, err := Paginate[nullableRow](db, pagination.NewPageRequest(1, 10), func(db *gorm.DB) *gorm.DB {
		return db.Model(&nullableRow{}).Select("name")
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Total != 3 {
		t.Errorf("Total = %d, want 3（COUNT(name) 漏计 NULL 行即 #136 复发）", resp.Total)
	}
	if len(resp.Items) != 3 {
		t.Errorf("len(Items) = %d, want 3", len(resp.Items))
	}
	if resp.Pages != 1 {
		t.Errorf("Pages = %d, want 1（Pages 按 Total 计算，偏一即 #136 复发）", resp.Pages)
	}
	// NULL 行仍在结果集里：投影列本列为 NULL 的行照常返回
	nullCount := 0
	for _, it := range resp.Items {
		if it.Name == nil {
			nullCount++
		}
	}
	if nullCount != 1 {
		t.Errorf("NULL name 行数 = %d, want 1（find 结果不应受计数语句改写影响）", nullCount)
	}
}

// TestPaginate_CountDropsSelects_WithWhere #136 的 Select+Where 组合形态
// （issue 实测矩阵之一）：条件过滤与投影共存时计数同样不得漏计 NULL 行。
func TestPaginate_CountDropsSelects_WithWhere(t *testing.T) {
	db := newTestDB(t, &nullableRow{})
	a, c := "a", "c"
	for _, r := range []*nullableRow{{Name: &a}, {}, {Name: &c}} {
		if err := db.Create(r).Error; err != nil {
			t.Fatal(err)
		}
	}

	resp, err := Paginate[nullableRow](db, pagination.NewPageRequest(1, 10), func(db *gorm.DB) *gorm.DB {
		return db.Model(&nullableRow{}).Select("name").Where("id > 0")
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Total != 3 || len(resp.Items) != 3 {
		t.Errorf("Total = %d len(Items) = %d, want 3/3（#136 复发）", resp.Total, len(resp.Items))
	}
}

// TestPaginate_CountThenFind_ProjectionPreserved 计数借还窗口的护栏：Count
// 结束后 find 路径仍按调用方的 Selects 投影执行——共享语句在计数后必须
// 恢复原状（Selects 还原），否则单列查询会悄悄退化成全列扫描。
func TestPaginate_CountThenFind_ProjectionPreserved(t *testing.T) {
	db := newTestDB(t, &nullableRow{})
	a := "a"
	if err := db.Create(&nullableRow{Name: &a}).Error; err != nil {
		t.Fatal(err)
	}

	resp, err := Paginate[nullableRow](db, pagination.NewPageRequest(1, 10), func(db *gorm.DB) *gorm.DB {
		return db.Model(&nullableRow{}).Select("name")
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Items) != 1 || resp.Items[0].Name == nil || *resp.Items[0].Name != "a" {
		t.Errorf("投影列取值异常: %+v", resp.Items)
	}
	if resp.Items[0].ID != 0 {
		t.Errorf("find 应仍按 Select(\"name\") 投影（ID 不应被扫入）, got ID=%d", resp.Items[0].ID)
	}
}

// ---- #103：Page 参数类型（Normalize + Offset + Limit 一体） ----

func TestNormalizePage_Defaults(t *testing.T) {
	for _, c := range [][2]int{{0, 0}, {-5, -1}} {
		p := NormalizePage(c[0], c[1])
		if p.Page() != pagination.DefaultPage || p.PageSize() != pagination.DefaultPageSize {
			t.Errorf("NormalizePage(%d, %d) = (%d, %d), want defaults (%d, %d)",
				c[0], c[1], p.Page(), p.PageSize(), pagination.DefaultPage, pagination.DefaultPageSize)
		}
	}
}

func TestNormalizePage_ClampToMax(t *testing.T) {
	p := NormalizePage(1, pagination.MaxPageSize*10)
	if p.PageSize() != pagination.MaxPageSize {
		t.Errorf("PageSize = %d, want clamp 到 %d", p.PageSize(), pagination.MaxPageSize)
	}
	p = NormalizePage(3, 10)
	if p.Page() != 3 || p.PageSize() != 10 {
		t.Errorf("合法值不应被改动: got (%d, %d), want (3, 10)", p.Page(), p.PageSize())
	}
}

func TestPage_OffsetLimit(t *testing.T) {
	p := NormalizePage(3, 10)
	if p.Offset() != 20 {
		t.Errorf("Offset = %d, want 20", p.Offset())
	}
	if p.Limit() != 10 {
		t.Errorf("Limit = %d, want 10", p.Limit())
	}
	// 溢出保护：page 来自外部可控参数，回绕偏移必须回退第一页
	p = NormalizePage(1<<62+2, 500)
	if p.Offset() != 0 {
		t.Errorf("overflow Offset = %d, want 0", p.Offset())
	}
}

func TestPage_Request(t *testing.T) {
	p := NormalizePage(2, 50)
	req := p.Request()
	if req.Page != 2 || req.PageSize != 50 {
		t.Errorf("Request() = %+v, want {2 50}", req)
	}
}

func TestPage_Scope_RealDB(t *testing.T) {
	db := newTestDB(t, &TestBase{})
	for i := 0; i < 25; i++ {
		if err := db.Create(&TestBase{Name: "row"}).Error; err != nil {
			t.Fatal(err)
		}
	}
	p := NormalizePage(2, 10) // 第 2 页
	var items []TestBase
	if err := db.Model(&TestBase{}).Scopes(p.Scope()).Order("id ASC").Find(&items).Error; err != nil {
		t.Fatal(err)
	}
	if len(items) != 10 {
		t.Fatalf("got %d items, want 10", len(items))
	}
	if items[0].ID != 11 || items[9].ID != 20 {
		t.Errorf("page 2 should be ids [11..20], got [%d..%d]", items[0].ID, items[9].ID)
	}
}
