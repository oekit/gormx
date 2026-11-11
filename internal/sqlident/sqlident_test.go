package sqlident

import "testing"

// TestMatch 固化单源白名单口径（#174）：字母/下划线开头，仅含字母、数字、
// 下划线与点。根包（SubqueryIn column）与 crud 包（Where/Or field）共用。
func TestMatch(t *testing.T) {
	allowed := []string{"id", "t.col", "_private", "Col_1.x", "a"}
	for _, s := range allowed {
		if !Match(s) {
			t.Errorf("Match(%q) = false, want true", s)
		}
	}
	rejected := []string{
		"",            // 空
		"1col",        // 数字开头
		"a,b",         // 逗号多列
		"a b",         // 空白
		"`key`",       // 反引号保留字（应走 WhereRaw）
		"count(*)",    // 表达式
		"(SELECT 1)",  // 子查询
		"t.col->>key", // json 路径
	}
	for _, s := range rejected {
		if Match(s) {
			t.Errorf("Match(%q) = true, want false", s)
		}
	}
}

// TestPlainPatternAnchored PlainPattern 必须带 ^/$ 全文锚点（错误文案引用
// 它展示完整白名单；复合白名单拼接用 PlainBody，不用带锚点的全文）。
func TestPlainPatternAnchored(t *testing.T) {
	if PlainPattern[0] != '^' || PlainPattern[len(PlainPattern)-1] != '$' {
		t.Fatalf("PlainPattern = %q, want 全文锚定", PlainPattern)
	}
}
