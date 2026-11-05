package gormx

import (
	"context"
	"errors"
	"fmt"

	"gorm.io/gorm"
)

// ErrAdvisoryLockUnsupported 是当前方言不支持会话级咨询锁的哨兵错误
// （errors.Is 可辨）。咨询锁的方言面：postgres=pg_advisory_lock、
// mysql=GET_LOCK；sqlite（单写者，无跨连接互斥需求）等不支持方言显式报错，
// 不静默退化成「无锁执行」——调用方要的是互斥，静默无锁比报错危险得多。
var ErrAdvisoryLockUnsupported = errors.New("gormx: advisory lock not supported by dialect")

// RunWithAdvisoryLock 把 fn 钉在**同一条连接**上执行，前后配对拿/释放会话级
// 咨询锁（[#79](https://github.com/oekit/gormx/issues/79)）。
//
// ## 为什么必须是库级原语
//
// 会话级咨询锁（pg_advisory_lock / MySQL GET_LOCK）的坑在连接池语义：
//
//	db.Exec("SELECT pg_advisory_lock(?)")         // 从池取连接 X 拿锁
//	defer db.Exec("SELECT pg_advisory_unlock(?)") // 从池取连接 Y——可能是另一条！
//
// 解锁落在另一条连接时是**空操作（不报错）**：锁随连接 X 归还池而泄漏（会话
// 级锁不随归还释放），第二个副本的阻塞式拿锁要等到持锁连接关闭（池 lifetime
// 分钟级）= 并发首启挂死。且 database/sql 池的 LIFO 空闲复用让单线程下解锁
// **大概率**落在同一条连接——缺陷单进程内近乎不可见、无任何报错，只在多副本 /
// 并发 churn 下显形，极难排查。正解 = gorm 的 Connection()（底层 *sql.Conn）
// 把 拿锁→执行→解锁 钉在同一条连接上——每个消费方都会独立踩一遍的坑，库级
// 单源一处钉死。
//
// ## 用法（多副本迁移串行化的标准形态）
//
//	if err := gormx.RunWithAdvisoryLock(ctx, db, migrateLockKey, func(tx *gorm.DB) error {
//	    // 互斥区：版本化迁移/幂等初始化——锁内第二副本阻塞等待
//	    return autoMigrate(tx)
//	}); err != nil {
//	    return err
//	}
//
// ## 语义契约
//
//   - 锁是**会话级**：fn 结束（含 panic）必然解锁（defer 落在持锁连接上）；
//     fn 拿到的 tx 与拿锁/解锁同一条连接，锁内语句都在该会话执行；
//   - 拿锁**阻塞**直到获得或 ctx 取消（取消中断阻塞并返回 ctx 错误——未拿到
//     锁的路径不做解锁）；
//   - 解锁走 context.Background()（收尾路径：调用方 ctx 已取消也要尽力解锁，
//     否则锁随连接归还而泄漏到连接关闭）；解锁失败打 ERROR——此时锁泄漏到
//     持锁连接关闭（池 lifetime 兜底），调用方告警面应覆盖该条日志；
//   - 方言：postgres → pg_advisory_lock/unlock（int64 键）；mysql →
//     GET_LOCK/RELEASE_LOCK（同键同坑，一并收编，键按十进制串语义隐式转换）；
//     其余方言（含 sqlite）返回 ErrAdvisoryLockUnsupported；
//   - 键为 int64：PG 咨询锁原生两态键（int64 单键形态），调用方用同一常量
//     跨副本对齐即可。
func RunWithAdvisoryLock(ctx context.Context, db *gorm.DB, key int64, fn func(tx *gorm.DB) error) error {
	if db == nil {
		return errors.New("gormx: RunWithAdvisoryLock db must not be nil")
	}
	if fn == nil {
		return errors.New("gormx: RunWithAdvisoryLock fn must not be nil")
	}
	lockSQL, unlockSQL, err := advisoryLockSQLs(db.Dialector.Name())
	if err != nil {
		return err
	}
	if ctx == nil {
		ctx = context.Background()
	}

	return db.Connection(func(conn *gorm.DB) error {
		// 拿锁——本连接内阻塞；ctx 取消中断（未拿到的路径无锁可解）
		if err := conn.WithContext(ctx).Exec(lockSQL, key).Error; err != nil {
			return fmt.Errorf("gormx: advisory lock acquire (key=%d): %w", key, err)
		}
		defer func() {
			// 解锁——同一条连接（本原语的存在理由）；收尾路径用
			// context.Background()，调用方 ctx 已取消也必须尽力解锁
			if uerr := conn.WithContext(context.Background()).Exec(unlockSQL, key).Error; uerr != nil {
				gormLog.Error("advisory unlock failed: 会话锁将泄漏到持锁连接关闭（池 lifetime 兜底）",
					"key", key, "err", uerr)
			}
		}()
		return fn(conn.WithContext(ctx))
	})
}

// advisoryLockSQLs 按方言返回 (拿锁, 解锁) SQL——分发逻辑独立成纯函数可单测
// （CI 无 PG/MySQL 服务，集成路径靠消费方实测，见 #79 的下游判据）。
func advisoryLockSQLs(dialect string) (lockSQL, unlockSQL string, err error) {
	switch dialect {
	case "postgres":
		// 会话级咨询锁 int64 单键形态；gorm 的参数绑定按方言改写占位符
		return "SELECT pg_advisory_lock(?)", "SELECT pg_advisory_unlock(?)", nil
	case "mysql":
		// GET_LOCK(str, timeout)：-1 = 无限阻塞（MySQL ≥5.7.5）；int64 键
		// 由 MySQL 隐式转十进制串——与 PG 共用 int64 键型，调用方无第二套 API
		return "SELECT GET_LOCK(?, -1)", "SELECT RELEASE_LOCK(?)", nil
	default:
		return "", "", fmt.Errorf("%w: %q（支持 postgres / mysql）", ErrAdvisoryLockUnsupported, dialect)
	}
}
