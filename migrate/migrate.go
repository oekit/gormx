// Package migrate 提供「裸 SQL 脚本 + 清单」的建表/升级执行器。
//
// 形态约定（与既有消费方互通，不可改）：
//   - 脚本清单**单层平铺**：install 与 upgrade 共用一份列表，按 Target 前缀选目录
//   - 幂等兜底：重复键/列/索引、建表「已存在」类错误一律跳过；多列 ALTER TABLE
//     ... ADD a, ADD b 失败时自动拆成单列逐条执行（MySQL 的多列 ADD 是原子的，
//     直接跳过会漏掉真正需要新增的列；非 ADD 子句不连坐 ADD 条目，head 也入
//     守卫，#164）
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
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"io/fs"
	"path"
	"reflect"
	"strings"

	"github.com/oekit/gormx"
	"github.com/oekit/gormx/internal/sqltext"
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
	// LockKey 非空时，Run 把全量执行包进 gormx.RunWithAdvisoryLock 的
	// 会话级咨询锁内（#98：多副本同时首启的 DDL 串行化组合口）。
	//
	//   - 空 = 不加锁（缺省行为不变；单副本/开发环境）；
	//   - 键为字符串，跨副本用**同一字符串**即互斥；建议口径取稳定业务
	//     维度（如 db 名 + 表清单哈希）——每库一个键会在多服务共用库时
	//     互锁，每 target/表一个键粒度又太细（本就该一起串行化的脚本
	//     被拆成并行）；
	//   - 内部经 FNV-1a 64 折成 int64 会话键（PG/MySQL 咨询锁键域），
	//     同一字符串必得同一 int64；不同字符串碰撞概率 2^-64 量级；
	//   - 拿锁阻塞直到获得或 ctx 取消（#177：RunContext 传入的 ctx 可中断
	//     无限阻塞的拿锁；旧 Run 入口用 Background，行为不变），拿锁失败/
	//     执行失败都返回错误 = fail-closed：交给部署的重启策略重试，
	//     比静默无锁执行安全（多副本并发 DDL 正是要防的事故形态）；
	//   - 方言不支持会话级咨询锁（sqlite 等）显式返回
	//     gormx.ErrAdvisoryLockUnsupported——与 RunWithAdvisoryLock 的
	//     方言策略对齐，不静默降级为无锁执行。
	LockKey string
}

// Run 依次执行 install 与 upgrade 全量脚本。
//
// 全量执行（而非记账只跑增量）是刻意的：脚本本身被设计成幂等
// （CREATE TABLE IF NOT EXISTS / INSERT IGNORE 等），重复执行靠幂等 + 异常兜底保证安全。
//
// Options.LockKey 非空时，全量执行在会话级咨询锁内串行化（多副本首启
// DDL 互斥，见 Options.LockKey 文档）。
//
// ⚠️ 无 ctx 兼容入口（#177）：等价于 RunContext(context.Background(), d, opts)
// ——DDL 不可取消、advisory lock 拿锁可无限阻塞。新代码请用 RunContext。
func Run(d *gorm.DB, opts Options) error {
	return RunContext(context.Background(), d, opts)
}

// RunContext 与 Run 相同，但 ctx 贯穿全链路（#177）：
//
//   - 每条语句经 d.WithContext(ctx).Exec 执行——ctx 取消/超时即中断，
//     DDL 不再「一旦卡住只能杀进程」；
//   - LockKey 路径把 ctx 传入 RunWithAdvisoryLock——MySQL GET_LOCK(?, -1)
//     的无限阻塞拿锁可被 ctx 取消中断（未拿到锁的路径无锁可解）。
//
// ctx 为 nil 时按 context.Background() 处理。
func RunContext(ctx context.Context, d *gorm.DB, opts Options) error {
	if ctx == nil {
		ctx = context.Background()
	}
	exec := func(tx *gorm.DB) error { return runScripts(ctx, tx, opts) }
	if opts.LockKey == "" {
		return exec(d)
	}
	return gormx.RunWithAdvisoryLock(ctx, d, lockKeyInt(opts.LockKey), exec)
}

// runScripts 依次执行 install 与 upgrade 清单。
func runScripts(ctx context.Context, d *gorm.DB, opts Options) error {
	for _, script := range opts.Install {
		if err := execScript(ctx, d, opts, script); err != nil {
			return err
		}
	}
	for _, script := range opts.Upgrade {
		if err := execScript(ctx, d, opts, script); err != nil {
			return err
		}
	}
	return nil
}

// lockKeyInt 把字符串锁键折叠为 int64 会话咨询锁键（FNV-1a 64）。
// 键域口径与 gormx.RunWithAdvisoryLock 一致（PG 咨询锁原生 int64 单键、
// MySQL GET_LOCK 十进制串语义），折叠是确定性的：同一字符串跨副本必然
// 得到同一 int64，互斥才成立。
func lockKeyInt(key string) int64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(key))
	return int64(h.Sum64())
}

// execScript 执行一个 SQL 文件。
func execScript(ctx context.Context, d *gorm.DB, opts Options, script string) error {
	// path.Join 而非字符串拼接：embed.FS 走 fs.ValidPath，"./mysql/x" 这种
	// 带前导成分的路径直接判不存在（Root="" 或 "." 的消费方会全量误报缺文件）。
	path := path.Join(opts.Root, opts.Target, script)
	content, err := fs.ReadFile(opts.FS, path)
	if err != nil {
		return fmt.Errorf("读取 SQL 文件 %s: %w", path, err)
	}
	logx.Delegating("gormx.migrate").Info("执行数据库脚本", "script", path)

	stmts, err := SplitStatements(string(content))
	if err != nil {
		return fmt.Errorf("SQL 切分失败 %s: %w", path, err)
	}
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
		if err := execStatement(ctx, d, path, i+1, stmt); err != nil {
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
// 语句经 d.WithContext(ctx).Exec 执行（#177）：ctx 取消/超时即中断。
func execStatement(ctx context.Context, d *gorm.DB, path string, idx int, stmt string) error {
	trimmed := strings.TrimSpace(stmt)
	if trimmed == "" {
		return nil
	}
	err := d.WithContext(ctx).Exec(trimmed).Error
	if err == nil {
		return nil
	}
	if !IsIgnorableError(err) {
		return fmt.Errorf("执行 SQL 文件 %s 第 %d 条语句失败: %w\nSQL: %s", path, idx, err, trimmed)
	}
	// 整条跳过前，先尝试把「多列 ADD」拆成单列逐条执行 ——
	// MySQL 的 ALTER TABLE ... ADD a, ADD b 是原子操作，只要有一列已存在就整条失败，
	// 直接跳过会导致「已存在的列」后面那些**真正需要新增的列也被漏掉**。
	if ok, splitErr := execSplitAlters(ctx, d, trimmed); ok {
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
// MySQL 的多列 ALTER 是原子操作，只要有一个子句失败（如某列已存在）就整条
// 失败——直接跳过会漏掉真正需要新增的列。拆分以「每条子句一个 ALTER」逐条
// 执行（牺牲子句间原子性，换取幂等重跑的可增量性）。
//
// 非 ADD 子句守卫（head 也入守卫，#164）：非 ADD 子句（如 DROP/CHANGE/MODIFY）
// 绝不与 ADD 条目合并进同一条拆分语句——合并携带时它的 ignorable 失败会把
// 要加的列整条连坐吞掉（迁移绿、schema 永久缺列）：
//
//   - tail（首个 ADD 之后的条目）混入非 ADD 子句：不做拆分（原契约口径，
//     走整条语句的幂等跳过路径）；
//   - head（首个 ADD 之前，#164 此前只查 parts 漏检 head）混入非 ADD 子句：
//     head 子句逐条**单独先执行**（失败 ignorable 跳过、否则上抛），ADD
//     条目只带 `ALTER TABLE <表>` 前缀裸拆——按 issue 修法二「head 的可
//     ignorable 子句单独先执行/剥离，不动它就不该连坐 ADD 条目」。
//
// 返回 (是否发生了拆分, 首个非幂等错误)。
func execSplitAlters(ctx context.Context, d *gorm.DB, stmt string) (bool, error) {
	prefix, clauses, ok := splitAlterTable(stmt)
	if !ok {
		return false, nil
	}
	// 以首个 ADD 切开 head/tail：之前的都是 head 子句，之后的必须全是 ADD
	firstAdd := -1
	for i, c := range clauses {
		if isAddClause(c) {
			firstAdd = i
			break
		}
	}
	if firstAdd < 0 || len(clauses) < 2 {
		return false, nil
	}
	headClauses := clauses[:firstAdd]
	addClauses := clauses[firstAdd:]

	// tail 守卫：ADD 条目之间混入非 ADD 子句（如 DROP/CHANGE/MODIFY）时不做拆分
	for _, c := range addClauses {
		if !isAddClause(c) {
			return false, nil
		}
	}

	// head 守卫（#164）：head 子句逐条单独先执行，绝不携带进 ADD 条目
	for _, c := range headClauses {
		if err := d.WithContext(ctx).Exec(prefix + " " + c).Error; err != nil && !IsIgnorableError(err) {
			return true, err
		}
	}
	for _, c := range addClauses {
		if err := d.WithContext(ctx).Exec(prefix + " " + c).Error; err != nil && !IsIgnorableError(err) {
			return true, err
		}
	}
	return true, nil
}

// splitAlterTable 把 `ALTER TABLE [ONLY|IF EXISTS] <表名> <子句…>` 切成
// 「ALTER TABLE <表名>」前缀与逐条子句（顶层逗号切分，引号/括号内不切）。
// 形状识别失败（非 ALTER TABLE、表名缺失、引号未闭合）返回 ok=false——
// 存疑时不做拆分，宁可走整条语句的幂等跳过路径，也不做猜测性拆分。
// 表名原样保留（标识符大小写语义随方言，不改写）。
func splitAlterTable(stmt string) (prefix string, clauses []string, ok bool) {
	s := strings.TrimSpace(stmt)
	const kw = "ALTER TABLE"
	if len(s) < len(kw) || !strings.EqualFold(s[:len(kw)], kw) {
		return "", nil, false
	}
	rest := s[len(kw):]
	for {
		rest = strings.TrimLeft(rest, " \t\r\n")
		if rest == "" {
			return "", nil, false
		}
		nameEnd := 0 // 表名 token 在 rest 中的结束位置（开区间）
		// 引号表名（反引号/双引号/单引号，双写引号为转义）
		if q := rest[0]; q == '`' || q == '"' || q == '\'' {
			nameEnd = 1
			for nameEnd < len(rest) {
				if rest[nameEnd] == q {
					if nameEnd+1 < len(rest) && rest[nameEnd+1] == q {
						nameEnd += 2
						continue
					}
					break
				}
				nameEnd++
			}
			if nameEnd >= len(rest) {
				return "", nil, false // 引号未闭合
			}
			nameEnd++ // 含右引号
		} else {
			// 裸词：ONLY / IF / EXISTS 是表名前的可选修饰（PG ONLY、MariaDB
			// IF EXISTS），其余即表名
			nameEnd = strings.IndexAny(rest, " \t\r\n,")
			if nameEnd < 0 {
				nameEnd = len(rest)
			}
			word := rest[:nameEnd]
			switch {
			case strings.EqualFold(word, "ONLY"),
				strings.EqualFold(word, "IF"),
				strings.EqualFold(word, "EXISTS"):
				rest = rest[nameEnd:]
				continue
			case word == "":
				return "", nil, false
			}
		}
		// 前缀按原句切片（保留修饰词与标识符大小写，不做改写）
		start := len(s) - len(rest)
		return s[:start+nameEnd], splitTopLevelClauses(rest[nameEnd:]), true
	}
}

// splitTopLevelClauses 按顶层逗号把 ALTER 子句列表切成单条（引号与括号内的
// 逗号不切；空片段丢弃）。
// 切分原语单源在 internal/sqltext（#175，带逃逸的正典版——此前本包是裸
// 引号状态机、无反斜杠逃逸处理，与根包 redact 的同语义实现健壮性分叉）：
// 含转义引号的子句（DEFAULT 'it\'s' 等）不再把逃逸后的引号误判为闭引号。
func splitTopLevelClauses(s string) []string {
	parts := sqltext.SplitTopLevel(s, ',')
	out := parts[:0]
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// isAddClause 报告子句是否 ADD 形态（大小写不敏感：ADD COLUMN/ADD INDEX/
// ADD KEY/ADD CONSTRAINT/ADD PRIMARY…）。
func isAddClause(c string) bool {
	return strings.HasPrefix(strings.ToUpper(strings.TrimSpace(c)), "ADD")
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
	for v.Kind() == reflect.Pointer {
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
func SplitStatements(content string) ([]string, error) {
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
			// 块注释：跳到 */；未闭合 = 脚本面不完整 ⇒ fail-closed（#230：
			// 此前游标静默推到文件尾，剩余语句既不进 code 也不报错，
			// 迁移报成功、schema 半成品）
			i += 2
			closed := false
			for i < len(runes) {
				if runes[i] == '*' && i+1 < len(runes) && runes[i+1] == '/' {
					i += 2
					closed = true
					break
				}
				i++
			}
			if !closed {
				return nil, fmt.Errorf("未闭合的块注释 /*(第 %d 列附近)——脚本不完整,拒绝执行", i)
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
	return statements, nil
}
