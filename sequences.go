// sequences.go —— PG 自增序列对齐（#226）。
//
// 背景：显式 ID 灌库形态（逐表 COPY/dbcopy/逻辑迁移）插入行但不推进序列，
// 序列停在低位后所有走自增的新插入永久撞主键冲突（PG 23505）。失败形态
// 是逐条冲突而非启动失败，且只打挂低流量表（高流量表靠撞墙烧 seq 迟早
// 越过 max「自愈」，更隐蔽）——argus 生产曾因此静默丢数两周（finding_prints
// 序列 990 vs max(id)=1157，2026-11-05 dbcopy 后至 11-21 修复）。
//
// 语义：对每个 serial/identity 序列，next value <= max(列) 即 setval(max+1)。
// 幂等、无副作用（序列前进是安全的；不动数据行）；健康序列零动作。
// SQLite 无此问题（rowid 按 max+1 自取）；MySQL AUTO_INCREMENT 在插入
// 撞已有 id 时同样会失败，但历史迁移未用显式 ID 灌库形态——如遇同类问题
// 按本文件同思路补方言分支。
//
// 不支持方言显式报错（同 advisory_lock 先例：维护诉求在不支持方言上
// 必须报错而不是静默跳过——静默会让调用方误以为已对齐）。
package gormx

import (
	"context"
	"errors"
	"fmt"

	"gorm.io/gorm"
)

// ErrSequenceRepairUnsupported 非 PG 方言请求序列对齐时返回。
// 调用方若想全方言统一调用，须自行按方言门控（与 advisory_lock 同口径）。
var ErrSequenceRepairUnsupported = errors.New("gormx: 序列对齐仅支持 postgres 方言")

// RepairSequences 对给定 GORM 连接（须为 PG）做序列对齐，返回被修复的
// 表清单（空 = 全部健康）。幂等：可每次启动调用，健康序列零动作。
//
// 实现：pg_depend 枚举全部 serial/identity 序列及其归属列；每序列读
// last_value/is_called 得 next；next <= 该列 max(id) 即 setval(max+1, false)
// （is_called=false 语义：下一个 nextval 恰好发 max+1）。空表 max=0 跳过。
func RepairSequences(ctx context.Context, db *gorm.DB) ([]string, error) {
	if db == nil {
		return nil, errors.New("gormx: RepairSequences 需要非 nil *gorm.DB")
	}
	dialector := db.Dialector
	if dialector == nil {
		return nil, errors.New("gormx: RepairSequences 需要已装配方言的 *gorm.DB")
	}
	if name := dialector.Name(); name != "postgres" {
		return nil, fmt.Errorf("%w（当前 %q）", ErrSequenceRepairUnsupported, name)
	}

	type seqPair struct {
		Seq   string
		Table string
		Col   string
	}
	var pairs []seqPair
	if err := db.WithContext(ctx).Raw(`
		SELECT s.relname AS seq, t.relname AS "table", a.attname AS col
		FROM pg_class s
		JOIN pg_depend d ON d.objid = s.oid AND d.deptype = 'a'
		JOIN pg_class t ON t.oid = d.refobjid
		JOIN pg_attribute a ON a.attrelid = t.oid AND a.attnum = d.refobjsubid
		WHERE s.relkind = 'S'`).Scan(&pairs).Error; err != nil {
		return nil, fmt.Errorf("gormx: 枚举序列失败: %w", err)
	}

	var repaired []string
	for _, p := range pairs {
		// 序列当前 next 值（is_called=false 时 last_value 即下一个待发值）。
		var lastValue int64
		var isCalled bool
		seqIdent := quoteIdent(p.Seq)
		if err := db.WithContext(ctx).Raw(
			fmt.Sprintf(`SELECT last_value, is_called FROM %s`, seqIdent)).
			Row().Scan(&lastValue, &isCalled); err != nil {
			return repaired, fmt.Errorf("gormx: 读序列 %s 失败: %w", p.Seq, err)
		}
		next := lastValue
		if isCalled {
			next = lastValue + 1
		}
		// 表当前最大 id（空表 0 → 无需修复）。
		var maxID int64
		if err := db.WithContext(ctx).Raw(
			fmt.Sprintf(`SELECT COALESCE(MAX(%s), 0) FROM %s`,
				quoteIdent(p.Col), quoteIdent(p.Table))).
			Row().Scan(&maxID); err != nil {
			return repaired, fmt.Errorf("gormx: 读 %s.%s 最大值失败: %w", p.Table, p.Col, err)
		}
		if maxID == 0 || next > maxID {
			continue
		}
		// setval 首参是序列名文本（regclass 字面量形态，非标识符——双引号钉
		// 会被当列名解析报错）；参数绑定交给 pgx 转义。setval(max+1, false)
		// 语义：下一个 nextval 恰好发 max+1。
		if err := db.WithContext(ctx).Exec(
			`SELECT setval(?, ?, false)`, p.Seq, maxID+1).Error; err != nil {
			return repaired, fmt.Errorf("gormx: setval %s 失败: %w", p.Seq, err)
		}
		repaired = append(repaired, p.Table)
	}
	return repaired, nil
}

// quoteIdent 双引号钉标识符（来自 pg_catalog，天然可信；仍按 PG 规则转义
// 内嵌引号防拼 SQL 面意外）。
func quoteIdent(id string) string {
	quoted := `"`
	for _, r := range id {
		if r == '"' {
			quoted += `""`
			continue
		}
		quoted += string(r)
	}
	return quoted + `"`
}
