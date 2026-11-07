package gormx

import "testing"

// 转义顺序即优先级（#114）：先 \ 后 % 再 _——顺序错会让 % 被二次转义。
func TestEscapeLike_OrderMatters(t *testing.T) {
	cases := []struct{ in, want string }{
		{`\`, `\\`},
		{`%`, `\%`},
		{`_`, `\_`},
		// \ 输入在 % 之前已先转义 ⇒ % 不会再把转义符本身当通配符处理
		{`\%`, `\\\%`},
		{`100%_done`, `100\%\_done`},
	}
	for _, c := range cases {
		if got := EscapeLike(c.in); got != c.want {
			t.Errorf("EscapeLike(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestLikeShapes(t *testing.T) {
	cases := []struct {
		contains, prefix, suffix, in string
	}{
		{`%a\%b%`, `a\%b%`, `%a\%b`, `a%b`},
	}
	for _, c := range cases {
		if got := LikeContains(c.in); got != c.contains {
			t.Errorf("LikeContains(%q) = %q, want %q", c.in, got, c.contains)
		}
		if got := LikePrefix(c.in); got != c.prefix {
			t.Errorf("LikePrefix(%q) = %q, want %q", c.in, got, c.prefix)
		}
		if got := LikeSuffix(c.in); got != c.suffix {
			t.Errorf("LikeSuffix(%q) = %q, want %q", c.in, got, c.suffix)
		}
	}
}

type escapeRow struct {
	ID   uint
	Name string
}

// 真查询语义：用户输入的 % 不再变全匹配、_ 不再变单字符通配（#114）。
func TestEscapeLike_QuerySemantics(t *testing.T) {
	db := newTestDB(t, &escapeRow{})
	rows := []escapeRow{
		{Name: "100%_done"}, {Name: "100_real_done"}, {Name: "plain"},
		{Name: "tail"}, {Name: "sail"},
	}
	if err := db.Create(&rows).Error; err != nil {
		t.Fatal(err)
	}

	var got []escapeRow

	// 包含字面 "0%_"：只命中第一行（未转义时 % 全匹配 + _ 单字符 ⇒ 三行全中）
	if err := db.Where("name LIKE ? ESCAPE '\\'", LikeContains("0%_")).Find(&got).Error; err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Name != "100%_done" {
		t.Errorf("LikeContains(\"0%%_\") 命中 %v, want [100%%_done]", got)
	}

	// 前缀 "tail"：不匹配 "sail"（含尾部 % 时也未转义成全匹配）
	if err := db.Where("name LIKE ? ESCAPE '\\'", LikePrefix("tail")).Find(&got).Error; err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Name != "tail" {
		t.Errorf("LikePrefix(\"tail\") 命中 %v, want [tail]", got)
	}

	// 后缀 "ail"：命中 sail 与 tail
	if err := db.Where("name LIKE ? ESCAPE '\\'", LikeSuffix("ail")).Find(&got).Error; err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Errorf("LikeSuffix(\"ail\") 命中 %d 行, want 2", len(got))
	}

	// 普通包含无通配符语义变化
	if err := db.Where("name LIKE ? ESCAPE '\\'", LikeContains("real")).Find(&got).Error; err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Errorf("LikeContains(\"real\") 命中 %d 行, want 1", len(got))
	}
}
