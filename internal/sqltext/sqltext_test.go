package sqltext

import (
	"reflect"
	"testing"
)

// TestSplitTopLevel 固化切分规则（#175 正典版）：引号（含逃逸）与括号内
// 的分隔符不切；段两侧空白裁剪；中段空段保留（列序对齐依赖下标稳定）、
// 尾段空段丢弃。
func TestSplitTopLevel(t *testing.T) {
	cases := []struct {
		name string
		in   string
		sep  byte
		want []string
	}{
		{"普通切分", "a, b, c", ',', []string{"a", "b", "c"}},
		{"引号内逗号不切", `'x,y', b`, ',', []string{"'x,y'", "b"}},
		{"括号内逗号不切", "f(a,b), c", ',', []string{"f(a,b)", "c"}},
		{"双写引号", `'a''b,c', d`, ',', []string{`'a''b,c'`, "d"}},
		{"反斜杠逃逸引号", `'a\',b', c`, ',', []string{`'a\',b'`, "c"}},
		{"反引号内逗号不切", "`t,x`, b", ',', []string{"`t,x`", "b"}},
		{"嵌套括号", "(a,(b,c)), d", ',', []string{"(a,(b,c))", "d"}},
		{"中段空段保留", "a,,c", ',', []string{"a", "", "c"}},
		{"尾段空段丢弃", "a,", ',', []string{"a"}},
		{"全空", "  ", ',', nil},
		{"未闭合引号吃到底（失败闭合方向）", "'unclosed, b", ',', []string{"'unclosed, b"}},
	}
	for _, c := range cases {
		got := SplitTopLevel(c.in, c.sep)
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: SplitTopLevel(%q) = %#v, want %#v", c.name, c.in, got, c.want)
		}
	}
}

// TestMatchParen 括号配对：引号内的括号/分隔符跳过，未配对返回 -1。
func TestMatchParen(t *testing.T) {
	cases := []struct {
		name string
		in   string
		open int
		want int
	}{
		{"普通配对", "(a,b)", 0, 5},
		{"引号内括号", "('a)b')", 0, 7},
		{"逃逸引号内括号", `('a\')b')`, 0, 9},
		{"嵌套", "((x))", 0, 5},
		{"未配对", "(a", 0, -1},
		{"引号未闭合", "('a", 0, -1},
	}
	for _, c := range cases {
		got := MatchParen(c.in, c.open)
		if got != c.want {
			t.Errorf("%s: MatchParen(%q, %d) = %d, want %d", c.name, c.in, c.open, got, c.want)
		}
	}
}

// TestSkipQuoted 引号区段末字节下标：双写引号仍在引号内、反斜杠转义下一
// 字节、反引号不适用反斜杠转义、未闭合吃到底。
func TestSkipQuoted(t *testing.T) {
	cases := []struct {
		name string
		in   string
		open int
		want int
	}{
		{"普通闭合", "'ab'", 0, 3},
		{"双写引号", `'a''b'`, 0, 5},
		{"反斜杠逃逸", `'a\'b'`, 0, 5},
		{"反引号不适用反斜杠", "`a\\`", 0, 3},
		{"未闭合吃到底", "'abc", 0, 3},
	}
	for _, c := range cases {
		got := SkipQuoted(c.in, c.open)
		if got != c.want {
			t.Errorf("%s: SkipQuoted(%q, %d) = %d, want %d", c.name, c.in, c.open, got, c.want)
		}
	}
}
