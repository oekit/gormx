// Package gormx 提供 GORM 数据库客户端封装。
//
// 统一封装：自定义 Logger、连接池配置、会话级咨询锁（RunWithAdvisoryLock：拿锁→执行→解锁钉在同一条连接上，#79）。
// 零可观测性副作用：构造不注入任何 tracing 插件。
//
// # 快速接入
//
// 基本用法：
//
//	db, err := gorm.Open(mysql.Open(dsn))
//	if err != nil {
//	    log.Fatal(err)
//	}
//
// 自定义连接池：
//
//	db, err := gorm.OpenWithPool(mysql.Open(dsn),
//	    10,              // maxIdleConns
//	    100,             // maxOpenConns
//	    30*time.Minute,  // connMaxLifetime
//	)
//
// # OTel 追踪（装饰器显式接入）
//
// 本包不做任何 tracing 注入。需要 SQL Span 时
// 用 github.com/oekit/tracing/gormotel 显式挂载（无 SDK 时 noop）：
//
//	db, err := gormx.Open(mysql.Open(dsn))
//	if err := db.Use(gormotel.NewPlugin(gormotel.WithDBName("mydb"))); err != nil {
//	    return err
//	}
//
// # 自定义 Logger
//
// 默认使用内置 Logger，支持：
//   - 慢查询阈值：默认 200ms
//   - 日志级别：默认 Info
package gormx
