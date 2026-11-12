package gormx

import (
	"fmt"
	"strings"

	"gorm.io/gorm"
	gclause "gorm.io/gorm/clause"
)

// 方言统一的轻量适配（gormx#189）：下游同时支持 MySQL 与 PostgreSQL 时不再
// 各自维护标识符引用与日期表达式分支。范围刻意收窄——
//   - 标识符引用：复用 GORM Dialector.QuoteTo（含限定名分段与标识符内引号
//     转义），不重造引用器；
//   - 日期表达式：「现在减 N 天」「今天减 N 天」「文本日期分桶」三个最高频
//     形态，业务值一律走绑定参数（gclause.Expr 的 SQL + Vars），不把外部输入
//     拼进 SQL；
//   - 不接管业务时区与领域专属语义（JSON/Casbin 等）——时间基准（NOW()/
//     now()/sqlite 'now'）的时区语义由数据库侧决定，消费方自行选择列存储
//     格式与时区策略。
//
// 支持方言：mysql / postgres / sqlite（sqlite 为 glebarez 与 gorm.io/driver/
// sqlite 两个 Dialector 的共同 Name()）。未知方言在构造期响亮 panic——静默
// 产出别家方言的 SQL 是运行期语法错误（甚至语义错误）远离根因的形态。

// QuoteIdentifier 按连接方言引用 SQL 标识符（表名/列名/索引名），复用
// GORM Dialector.QuoteTo——含标识符内引号转义（mysql “ 转义等）与限定名
// 分段（"."分隔的每段分别引用，gormx#189）：下游不再手拼反引号/双引号，
// 保留字（user/order/group…）与含引号的标识符两后端同形态安全。
//
//   - db 为 nil（或 Dialector 为 nil）时 panic：引用字符是方言契约，无连接
//     可言时猜测引用字符是注入面，不做静默回退；
//   - name 为空串返回空串（no-op）；
//   - 限定名按段分别引用（如 "schema.user" → mysql "`schema`.`user`"）；
//   - 已带方言引号的标识符原样保留引号（QuoteTo 语义）。
//
// 典型用法：动态 ORDER BY / 聚合列名来自配置或前端时，必须引用后再拼进语句：
//
//	db.Order(gormx.QuoteIdentifier(db, sortColumn) + " DESC")
func QuoteIdentifier(db *gorm.DB, name string) string {
	requireDialector(db, "QuoteIdentifier")
	if name == "" {
		return ""
	}
	var b strings.Builder
	dialectorOf(db).QuoteTo(&b, name)
	return b.String()
}

// NowMinusDays 返回「当前时刻减 N 天」的方言统一表达式（datetime 语义，
// gormx#189）。days 是绑定参数（不在 SQL 文本内）：
//
//	mysql:    DATE_SUB(NOW(), INTERVAL ? DAY)
//	postgres: now() - make_interval(days => ?::int)
//	sqlite:   datetime('now', '-' || ? || ' days')
//
// 用法：db.Where(gormx.NowMinusDays(db, 30)) ⇒ "WHERE created_at < ?表达式?"。
func NowMinusDays(db *gorm.DB, days int) gclause.Expr {
	return dateDeltaExpr(db, false, days)
}

// TodayMinusDays 返回「今天减 N 天」的方言统一表达式（date 语义，当天零点
// 起算，gormx#189）。days 是绑定参数：
//
//	mysql:    DATE_SUB(CURRENT_DATE(), INTERVAL ? DAY)
//	postgres: CURRENT_DATE - make_interval(days => ?::int)
//	sqlite:   date('now', '-' || ? || ' days')
//
// 与 [NowMinusDays] 的差别是时钟面：NOW() 含当前时刻（滚动 24h×N），
// CURRENT_DATE/date('now') 取当天零点（自然日边界）。业务按「自然日」计
// 算（如「近 30 天订单」跨时区语义）用本表达式。
func TodayMinusDays(db *gorm.DB, days int) gclause.Expr {
	return dateDeltaExpr(db, true, days)
}

// DateBucket 返回把日期/时间列取整为「YYYY-MM-DD 文本」的方言统一表达式
// （gormx#189），用于按天分桶聚合（GROUP BY / 比较日期边界）；column 经
// [QuoteIdentifier] 引用：
//
//	mysql:    DATE(`col`)
//	postgres: to_char("col", 'YYYY-MM-DD')
//	sqlite:   date(`col`)
//
// column 语义：日期/时间列（DATETIME/TIMESTAMP/timestamptz/date）。sqlite
// 的 date() 要求列值为 ISO8601 形态（'YYYY-MM-DD…'）；text 列存非 ISO 形态
// 时分桶为 NULL——列存储格式是调用方契约，本助手不接管。
func DateBucket(db *gorm.DB, column string) gclause.Expr {
	requireDialector(db, "DateBucket")
	if column == "" {
		panic("gormx: DateBucket requires a non-empty column name")
	}
	col := QuoteIdentifier(db, column)
	d := dialectorOf(db)
	switch d.Name() {
	case "mysql":
		return gclause.Expr{SQL: "DATE(" + col + ")"}
	case "postgres":
		return gclause.Expr{SQL: "to_char(" + col + ", 'YYYY-MM-DD')"}
	case "sqlite":
		return gclause.Expr{SQL: "date(" + col + ")"}
	default:
		panic(fmt.Sprintf("gormx: DateBucket does not support dialect %q (mysql/postgres/sqlite only, gormx#189)", d.Name()))
	}
}

// dialectorOf 返回连接的 Dialector（requireDialector 校验后恒非 nil）：
// 经函数出口取用而非 db.Dialector.X 链式选择器——Config.Dialector 字段名与
// 类型同名，链式写法会被 staticcheck QF1008 判成冗余嵌入层穿透。
func dialectorOf(db *gorm.DB) gorm.Dialector {
	return db.Dialector
}

// dateDeltaExpr 是 NowMinusDays/TodayMinusDays 的方言分派单源：SQL 文本静态、
// days 走绑定参数（各驱动DryRun 参数绑定见 dialect_test.go）。
func dateDeltaExpr(db *gorm.DB, today bool, days int) gclause.Expr {
	requireDialector(db, "date delta")
	name := dialectorOf(db).Name()
	switch name {
	case "mysql":
		if today {
			return gclause.Expr{SQL: "DATE_SUB(CURRENT_DATE(), INTERVAL ? DAY)", Vars: []any{days}}
		}
		return gclause.Expr{SQL: "DATE_SUB(NOW(), INTERVAL ? DAY)", Vars: []any{days}}
	case "postgres":
		if today {
			return gclause.Expr{SQL: "CURRENT_DATE - make_interval(days => ?::int)", Vars: []any{days}}
		}
		return gclause.Expr{SQL: "now() - make_interval(days => ?::int)", Vars: []any{days}}
	case "sqlite":
		if today {
			return gclause.Expr{SQL: "date('now', '-' || ? || ' days')", Vars: []any{days}}
		}
		return gclause.Expr{SQL: "datetime('now', '-' || ? || ' days')", Vars: []any{days}}
	default:
		panic(fmt.Sprintf("gormx: date delta does not support dialect %q (mysql/postgres/sqlite only, gormx#189)", name))
	}
}

// requireDialector 是方言助手的统一 nil 守卫：db 或其 Dialector 为 nil 时
// panic（fail-fast）——引用字符与日期语法都是方言契约，无连接可依时猜测
// 产出是注入面/语法错误远离根因的形态，不静默回退。
func requireDialector(db *gorm.DB, what string) {
	if db == nil || db.Dialector == nil {
		panic(fmt.Sprintf("gormx: %s requires a non-nil *gorm.DB with a Dialector (dialect-specific SQL cannot be guessed safely)", what))
	}
}
