package gormx

import "testing"

// TestRedactorBrokenFailsClosed（#178 回归）：正则编译失败的 redactor
// 必须 fail-closed——整句替换为 ***，绝不原文放行。旧实现编译失败返回
// nil，与「未配置脱敏列」不可区分：敏感列配置存在却整体失效，Trace 日志
// 照常输出完整绑定值（#106 要防的泄漏面静默重开）且无任何信号。
func TestRedactorBrokenFailsClosed(t *testing.T) {
	r := &redactor{
		columns: map[string]struct{}{"password": {}},
		broken:  true,
	}
	cases := []string{
		"UPDATE t SET password = 'leak' WHERE id = 1",
		"INSERT INTO t (id, password) VALUES (1, 'leak')",
		"SELECT * FROM t",
	}
	for _, sql := range cases {
		if got := r.redact(sql); got != "***" {
			t.Errorf("broken redactor redact(%q) = %q, want \"***\"（fail-closed）", sql, got)
		}
	}
}

// TestNewRedactorNeverNilWithColumns #178 的构造面钉住：只要配置了敏感列
// （含正则元字符、引号等怪异列名——QuoteMeta 后均合法），newRedactor 必须
// 返回可用的非 nil redactor 且正常脱敏；「返回 nil」只允许出现在空集
// （等价不脱敏，零开销直通）。
func TestNewRedactorNeverNilWithColumns(t *testing.T) {
	for _, cols := range [][]string{
		{"password"},
		{"pass(word)", "back`tick"}, // 正则元字符/引号列名（QuoteMeta 兜住）
		{"Password", "password"},    // 大小写归一 + 去重
		{"  ", ""},                  // 规范化后为空的列名被剔除 → 空集 → nil
	} {
		r := newRedactor(cols)
		nonEmpty := false
		for _, c := range cols {
			if normalizeColumnName(c) != "" {
				nonEmpty = true
				break
			}
		}
		if !nonEmpty {
			if r != nil {
				t.Errorf("newRedactor(%q) = 非 nil, 空集应返回 nil", cols)
			}
			continue
		}
		if r == nil {
			t.Fatalf("newRedactor(%q) = nil, 配置敏感列时绝不返回 nil（#178）", cols)
		}
		if r.broken {
			t.Fatalf("newRedactor(%q) 不应进入 broken（QuoteMeta 后编译不可失败）", cols)
		}
		probe := "UPDATE t SET " + cols[0] + " = 'leak'"
		if got := r.redact(probe); got == probe {
			t.Errorf("newRedactor(%q) 未脱敏赋值形态: %q", cols, got)
		}
	}
}
