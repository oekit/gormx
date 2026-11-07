// Package migrate 提供「裸 SQL 脚本 + 清单」的建表/升级执行器。
//
// 形态约定（与既有消费方互通，不可改）：
//   - 脚本清单**单层平铺**：install 与 upgrade 共用一份列表，按 Target 前缀选目录
//   - 幂等兜底：重复键/列/索引、建表「已存在」类错误一律跳过；多列 ALTER TABLE
//     ... ADD a, ADD b 失败时自动拆成单列逐条执行（MySQL 的多列 ADD 是原子的，
//     直接跳过会漏掉真正需要新增的列）
//
// # 已知局限：不支持 DELIMITER / 存储过程脚本（#104）
//
// SplitStatements 按「引号外分号切分」工作，没有 DELIMITER 状态机——MySQL
// 存储过程/触发器脚本（DELIMITER $$ ... END$$ DELIMITER ;）的过程体内分号
// 会被当作语句终止符逐条拆出，必然语法报错导致迁移中止。此类脚本请经专用
// 迁移通道；执行中命中 DELIMITER 指令时打 WARN 指明根因（否则消费方看到的
// 是过程体第一句的语法错误，不会指向切分器局限）。
package migrate

import (
	"errors"
	"fmt"
	"io/fs"
	"path"
	"reflect"
	"strings"

	"github.com/oekit/logx"
	"gorm.io/gorm"
)

// Options 是一次迁移执行的配置。
type Options struct {
	// Target 目标标识（如 "mysql" / "postgre"），用于拼脚本目录 <Root>/<Target>/<script>。
	Target string
	// Install 安装脚本清单（按序全量执行）。
	Install []string
	// Upgrade 升级脚本清单（install 之后按序全量执行）。
	Upgrade []string
	// FS 脚本来源（embed.FS / os.DirFS / fstest.MapFS 均可）。
	FS fs.FS
	// Root 脚本根目录（FS 内的相对路径，如 "ddl"）。
	Root string
}

// Run 依次执行 install 与 upgrade 全量脚本。
//
// 全量执行（而非记账只跑增量）是刻意的：脚本本身被设计成幂等
// （CREATE TABLE IF NOT EXISTS / INSERT IGNORE 等），重复执行靠幂等 + 异常兜底保证安全。
func Run(d *gorm.DB, opts Options) error {
	for _, script := range opts.Install {
		if err := execScript(d, opts, script); err != nil {
			return err
		}
	}
	for _, script := range opts.Upgrade {
		if err := execScript(d, opts, script); err != nil {
			return err
		}
	}
	return nil
}

// execScript 执行一个 SQL 文件。
func execScript(d *gorm.DB, opts Options, script string) error {
	// path.Join 而非字符串拼接：embed.FS 走 fs.ValidPath，"./mysql/x" 这种
	// 带前导成分的路径直接判不存在（Root="" 或 "." 的消费方会全量误报缺文件）。
	path := path.Join(opts.Root, opts.Target, script)
	content, err := fs.ReadFile(opts.FS, path)
	if err != nil {
		return fmt.Errorf("读取 SQL 文件 %s: %w", path, err)
	}
	logx.Delegating("gormx.migrate").Info("执行数据库脚本", "script", path)

	stmts := SplitStatements(string(content))
	warnedDelimiter := false
	for i, stmt := range stmts {
		// DELIMITER 指令出现 = 脚本面超出切分器能力（#104）：fail-loud 之前
		// 先把根因说清楚——后续碎片执行报的是过程体语法错误，不指向这里。
		if !warnedDelimiter && isDelimiterDirective(stmt) {
			logx.Delegating("gormx.migrate").Warn(
				"脚本含 DELIMITER 指令：SplitStatements 不支持 DELIMITER/存储过程脚本（过程体内的分号会被当作语句终止符逐条拆出、必然语法报错）——此类脚本请经专用迁移通道处理",
				"script", path)
			warnedDelimiter = true
		}
		if err := execStatement(d, path, i+1, stmt); err != nil {
			return err
		}
	}
	return nil
}

// isDelimiterDirective 报告语句是否以 DELIMITER 指令开头（MySQL 客户端指令，
// 大小写不敏感）。它不是 SQL 语句——出现即说明脚本依赖 DELIMITER 语义。
func isDelimiterDirective(stmt string) bool {
	fields := strings.Fields(strings.TrimSpace(stmt))
	return len(fields) > 0 && strings.EqualFold(fields[0], "DELIMITER")
}

// execStatement 执行单条 SQL，必要时做幂等降级。
func execStatement(d *gorm.DB, path string, idx int, stmt string) error {
	trimmed := strings.TrimSpace(stmt)
	if trimmed == "" {
		return nil
	}
	err := d.Exec(trimmed).Error
	if err == nil {
		return nil
	}
	if !IsIgnorableError(err) {
		return fmt.Errorf("执行 SQL 文件 %s 第 %d 条语句失败: %w\nSQL: %s", path, idx, err, trimmed)
	}
	// 整条跳过前，先尝试把「多列 ADD」拆成单列逐条执行 ——
	// MySQL 的 ALTER TABLE ... ADD a, ADD b 是原子操作，只要有一列已存在就整条失败，
	// 直接跳过会导致「已存在的列」后面那些**真正需要新增的列也被漏掉**。
	if ok, splitErr := execSplitAlters(d, trimmed); ok {
		if splitErr != nil {
			return fmt.Errorf("执行 SQL 文件 %s 第 %d 条语句（拆分后）失败: %w\nSQL: %s", path, idx, splitErr, trimmed)
		}
		logx.Delegating("gormx.migrate").Debug("多列 ALTER 已拆分执行", "script", path, "index", idx)
		return nil
	}
	logx.Delegating("gormx.migrate").Debug("跳过幂等语句", "script", path, "index", idx)
	return nil
}

// execSplitAlters 把 `ALTER TABLE t ADD a ..., ADD b ...` 拆成多条单列 ALTER 逐条执行。
//
// 返回 (是否发生了拆分, 首个非幂等错误)。
func execSplitAlters(d *gorm.DB, stmt string) (bool, error) {
	upper := strings.ToUpper(stmt)
	if !strings.HasPrefix(upper, "ALTER TABLE") || !strings.Contains(upper, " ADD ") {
		return false, nil
	}
	// 取 `ADD` 之后到语句末尾的部分
	idx := strings.Index(upper, " ADD ")
	if idx < 0 {
		return false, nil
	}
	head := strings.TrimSpace(stmt[:idx])
	tail := stmt[idx:]

	// 切分 ADD 列表（顶层逗号）
	var parts []string
	depth := 0
	cur := strings.Builder{}
	inQuote := byte(0)
	for i := 0; i < len(tail); i++ {
		c := tail[i]
		if inQuote != 0 {
			cur.WriteByte(c)
			if c == inQuote {
				inQuote = 0
			}
			continue
		}
		switch c {
		case '\'', '"', '`':
			inQuote = c
			cur.WriteByte(c)
		case '(':
			depth++
			cur.WriteByte(c)
		case ')':
			depth--
			cur.WriteByte(c)
		case ',':
			if depth == 0 {
				parts = append(parts, cur.String())
				cur.Reset()
				continue
			}
			cur.WriteByte(c)
		default:
			cur.WriteByte(c)
		}
	}
	if strings.TrimSpace(cur.String()) != "" {
		parts = append(parts, cur.String())
	}
	if len(parts) < 2 {
		return false, nil
	}

	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if !strings.HasPrefix(strings.ToUpper(p), "ADD") {
			// 含非 ADD 子句（如 DROP/CHANGE/MODIFY）时不做拆分
			return false, nil
		}
		single := head + " " + p
		if err := d.Exec(single).Error; err != nil {
			if IsIgnorableError(err) {
				continue
			}
			return true, err
		}
	}
	return true, nil
}

// IsIgnorableError 判断异常是否属于「重复执行可跳过」。
//
// 判定面两级（#96，对齐根包 IsBusyErr 的结构论证：驱动文案**不是契约**——
// MySQL 驱动升级、PG 本地化 locale（lc_messages 非英语时错误文案整句变型）、
// proxy/中间层改写都会动文案，且文本匹配双向危险：漏匹配让幂等错误中断
// 迁移，过宽匹配让致命错误被静默跳过、schema 缺东西而迁移「成功」）：
//
//  1. 类型化主判（isIgnorableTyped）：MySQL 错误号（*mysql.MySQLError.Number：
//     1050/1060/1061/1062/1091）与 PG SQLSTATE（*pgconn.PgError.SQLState()：
//     42P07/42P01/42701/42710/23505）——码是协议层契约，不受文案漂移影响；
//  2. 文案兜底（最后防线）：下方英文子串表，仅覆盖类型化路径够不到的
//     驱动/方言（如 sqlite 的 "already exists"）。表内容不是契约，
//     驱动改文案后只剩它时判定即退化——不要反向依赖它。
//
// 两级判据都不命中时按致命错误处理（不跳过、显式报错）。
func IsIgnorableError(err error) bool {
	if err == nil {
		return false
	}
	if isIgnorableTyped(err) {
		return true
	}
	msg := strings.ToLower(err.Error())
	for _, needle := range []string{
		"already exists",
		"duplicate column name",
		"duplicate key name",
		"unknown index",
		"check that column/key exists",
		"check that it exists",
		"duplicate entry",
		"duplicate key",
		"duplicate table",
		"duplicate index",
		"relation already exists",
		"already been created",
	} {
		if strings.Contains(msg, needle) {
			return true
		}
	}
	return false
}

// pgSQLStater 是 *pgconn.PgError 的局部接口投影：不 import pgx（本模块零
// pg 驱动链接），SQLState() 方法签名与 pgconn.PgError 一致，errors.As 沿
// 错误链命中（gorm/包装层透明）。
type pgSQLStater interface {
	SQLState() string
}

// ignorablePGCodes 是「重复执行可跳过」的 PostgreSQL SQLSTATE 白名单：
//
//	42P07 duplicate_table（建表已存在）
//	42P01 undefined_table（DROP 不存在的表）
//	42701 duplicate_column（加列已存在）
//	42710 duplicate_object（CREATE OR REPLACE 之外的重复对象）
//	23505 unique_violation（幂等 INSERT 撞唯一键）
var ignorablePGCodes = map[string]bool{
	"42P07": true,
	"42P01": true,
	"42701": true,
	"42710": true,
	"23505": true,
}

// ignorableMySQLNumbers 是「重复执行可跳过」的 MySQL 错误号白名单：
//
//	1050 Table 'x' already exists
//	1060 Duplicate column name
//	1061 Duplicate key name
//	1062 Duplicate entry
//	1091 Can't DROP ... ; check that column/key exists
var ignorableMySQLNumbers = map[uint16]bool{
	1050: true,
	1060: true,
	1061: true,
	1062: true,
	1091: true,
}

// isIgnorableTyped 是类型化主判（#96）：码命中白名单才算，未命中**不**回落
// 文案（由调用方统一兜底）——类型可辨而码不在白名单的（如 PG 42601 语法错）
// 恰是必须显式报错的形态。
func isIgnorableTyped(err error) bool {
	var pg pgSQLStater
	if errors.As(err, &pg) && ignorablePGCodes[pg.SQLState()] {
		return true
	}
	if n, ok := mysqlErrorNumber(err); ok {
		return ignorableMySQLNumbers[n]
	}
	return false
}

// mysqlErrorNumber 沿 errors 链（含 Unwrap() []error 的多路树）查找
// github.com/go-sql-driver/mysql 的 *MySQLError 并返回其错误号。
//
// MySQLError 以**导出字段** Number uint16（而非方法）暴露错误码——局部接口
// 断言够不到，按「指针结构体、类型名 MySQLError、Number 字段 uint16」的
// 形状经反射读取（只读形状，不 import 驱动包、不做任何驱动调用，保持本模块
// 零驱动链接的口径）。
func mysqlErrorNumber(err error) (uint16, bool) {
	for err != nil {
		if n, ok := mysqlErrorNumberOne(err); ok {
			return n, true
		}
		switch x := err.(type) {
		case interface{ Unwrap() error }:
			err = x.Unwrap()
		case interface{ Unwrap() []error }:
			for _, sub := range x.Unwrap() {
				if n, ok := mysqlErrorNumber(sub); ok {
					return n, true
				}
			}
			return 0, false
		default:
			return 0, false
		}
	}
	return 0, false
}

// mysqlErrorNumberOne 对单个错误值做形状判定并读取 Number 字段。
func mysqlErrorNumberOne(err error) (uint16, bool) {
	v := reflect.ValueOf(err)
	for v.Kind() == reflect.Ptr {
		if v.IsNil() {
			return 0, false
		}
		v = v.Elem()
	}
	if v.Kind() != reflect.Struct || v.Type().Name() != "MySQLError" {
		return 0, false
	}
	f := v.FieldByName("Number")
	if f.IsValid() && f.Kind() == reflect.Uint16 {
		return uint16(f.Uint()), true
	}
	return 0, false
}

// SplitStatements 把 SQL 文件切成单条语句。
//
// 规则（与 Spring 复刻消费方 splitStatements 同规则）：
//   - `--` 行注释、`/* */` 块注释被剥离（随语句一起丢弃）
//   - 单引号 / 双引号 / 反引号内的分号不切分（反斜杠转义引号可正确跳过）
//   - 以 `;` 结尾为一条语句；空语句丢弃
//
// ⚠️ 不支持 DELIMITER / 存储过程脚本（#104，见包文档「已知局限」）：
// 没有 DELIMITER 状态机，过程体内的分号会被当作语句终止符逐条拆出。
func SplitStatements(content string) []string {
	var statements []string
	var code strings.Builder

	runes := []rune(content)
	i := 0
	for i < len(runes) {
		c := runes[i]
		next := rune(0)
		if i+1 < len(runes) {
			next = runes[i+1]
		}

		switch {
		case c == '-' && next == '-':
			// 行注释：跳过到行尾
			for i < len(runes) && runes[i] != '\n' {
				i++
			}
		case c == '/' && next == '*':
			// 块注释：跳到 */
			i += 2
			for i < len(runes) {
				if runes[i] == '*' && i+1 < len(runes) && runes[i+1] == '/' {
					i += 2
					break
				}
				i++
			}
		case c == '\'' || c == '"' || c == '`':
			// 引号内原样保留
			quote := c
			code.WriteRune(c)
			i++
			for i < len(runes) {
				if runes[i] == '\\' && i+1 < len(runes) {
					code.WriteRune(runes[i])
					code.WriteRune(runes[i+1])
					i += 2
					continue
				}
				code.WriteRune(runes[i])
				if runes[i] == quote {
					i++
					break
				}
				i++
			}
		case c == ';':
			if strings.TrimSpace(code.String()) != "" {
				statements = append(statements, code.String())
			}
			code.Reset()
			i++
		default:
			code.WriteRune(c)
			i++
		}
	}
	if strings.TrimSpace(code.String()) != "" {
		statements = append(statements, code.String())
	}
	return statements
}
