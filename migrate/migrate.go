// Package migrate 提供「裸 SQL 脚本 + 清单」的建表/升级执行器。
//
// 形态约定（与既有消费方互通，不可改）：
//   - 脚本清单**单层平铺**：install 与 upgrade 共用一份列表，按 Target 前缀选目录
//   - 幂等兜底：重复键/列/索引、建表「已存在」类错误一律跳过；多列 ALTER TABLE
//     ... ADD a, ADD b 失败时自动拆成单列逐条执行（MySQL 的多列 ADD 是原子的，
//     直接跳过会漏掉真正需要新增的列）
package migrate

import (
	"fmt"
	"io/fs"
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
	path := opts.Root + "/" + opts.Target + "/" + script
	content, err := fs.ReadFile(opts.FS, path)
	if err != nil {
		return fmt.Errorf("读取 SQL 文件 %s: %w", path, err)
	}
	logx.Delegating("gormx.migrate").Info("执行数据库脚本", "script", path)

	stmts := SplitStatements(string(content))
	for i, stmt := range stmts {
		if err := execStatement(d, path, i+1, stmt); err != nil {
			return err
		}
	}
	return nil
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
// 白名单对齐 Spring 复刻消费方的既有实现（文案大小写不敏感）：
//
//	"already exists"                    PG 42P07 / 通用
//	"duplicate column name"             MySQL 1060
//	"duplicate key name"                MySQL 1061
//	"unknown index"                     MySQL 1091
//	"check that column/key exists"      MySQL 1091
//	"check that it exists"              MySQL 1091
//	"duplicate entry" / "duplicate key" / "duplicate table" / "duplicate index"
//	"relation already exists" / "already been created"
func IsIgnorableError(err error) bool {
	if err == nil {
		return false
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

// SplitStatements 把 SQL 文件切成单条语句。
//
// 规则（与 Spring 复刻消费方 splitStatements 同规则）：
//   - `--` 行注释、`/* */` 块注释被剥离（随语句一起丢弃）
//   - 单引号 / 双引号 / 反引号内的分号不切分（反斜杠转义引号可正确跳过）
//   - 以 `;` 结尾为一条语句；空语句丢弃
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
