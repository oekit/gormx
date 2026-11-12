package gormx

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/oekit/logx"
	gormLogger "gorm.io/gorm/logger"
	"gorm.io/gorm/utils"
)

// Logger 是基于 logx 的 GORM 日志适配器，实现 gormLogger.Interface 接口。
// 通过 logx 门面输出，支持 slog 结构化日志和 trace_id 传播。
type Logger struct {
	log    logx.Logger
	redact *redactor
	gormLogger.Config
}

// LoggerOption 配置 Logger 的行为（函数选项模式）。
type LoggerOption func(*Logger)

// WithRedactColumns 配置 SQL 日志的列级脱敏（#106）：Trace 输出的语句中，
// 命中列名（大小写/引号不敏感）的绑定值替换为 ***，语句结构保留。
//
// 覆盖 UPDATE/DELETE 的 SET/WHERE 赋值与 INSERT 元组（含多行）；明细与
// 边界见 redactor 文档。GORM Info 级日志含完整绑定值——password/token/
// request_body 等敏感列应配置脱敏，而不是整体关掉 SQL 日志（丢排障价值）。
func WithRedactColumns(cols ...string) LoggerOption {
	return func(l *Logger) {
		l.redact = newRedactor(cols)
	}
}

// NewLogger 根据给定的配置创建 Logger 实例。
// 内部持有 Delegating logger 懒解析默认后端：构造（gormx.Open）先于业务
// logx.SetDefault 时，SQL 日志仍能跟随运行期切换的后端，不会固化到构造
// 时刻的 Default。
func NewLogger(config gormLogger.Config, opts ...LoggerOption) *Logger {
	l := &Logger{
		log:    logx.Delegating("gormx"),
		Config: config,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(l)
		}
	}
	return l
}

// NewDefaultLogger 创建带有合理默认配置（慢查询阈值 200ms、Info 级别）的 Logger。
//
// ⚠️ Info 级 SQL 日志**含完整绑定值**（INSERT/UPDATE 的 VALUES 全量外泄，
// 日志采集外送把泄漏面放大到 DB 之外）——敏感列经 WithRedactColumns 脱敏：
//
//	gormx.Open(mysql.Open(dsn), gormx.WithLogger(
//	    gormx.NewDefaultLogger(gormx.WithRedactColumns("password", "token", "request_body"))))
func NewDefaultLogger(opts ...LoggerOption) *Logger {
	config := gormLogger.Config{
		SlowThreshold:             200 * time.Millisecond,
		LogLevel:                  gormLogger.Info,
		IgnoreRecordNotFoundError: true,
		Colorful:                  false,
	}
	return NewLogger(config, opts...)
}

// LogMode 返回一个设置了新日志级别的 Logger 副本。
func (l *Logger) LogMode(level gormLogger.LogLevel) gormLogger.Interface {
	newLogger := *l
	newLogger.LogLevel = level
	return &newLogger
}

// Info 以 Info 级别记录日志。
func (l *Logger) Info(ctx context.Context, msg string, data ...any) {
	l.log.CtxInfof(ctx, msg, data...)
}

// Warn 以 Warn 级别记录日志。
func (l *Logger) Warn(ctx context.Context, msg string, data ...any) {
	l.log.CtxWarnf(ctx, msg, data...)
}

// Error 以 Error 级别记录日志。
func (l *Logger) Error(ctx context.Context, msg string, data ...any) {
	l.log.CtxErrorf(ctx, msg, data...)
}

// Trace 根据耗时和错误情况，以相应级别记录 SQL 追踪日志。
// 通过 logx 结构化日志输出，包含调用位置、耗时、行数、SQL 等字段。
func (l *Logger) Trace(ctx context.Context, begin time.Time, fc func() (sql string, rowsAffected int64), err error) {
	if l.LogLevel <= gormLogger.Silent {
		return
	}

	elapsed := time.Since(begin)
	caller := utils.FileWithLineNum()
	elapsedMs := float64(elapsed.Nanoseconds()) / 1e6

	switch {
	case err != nil && l.LogLevel >= gormLogger.Error && (!errors.Is(err, gormLogger.ErrRecordNotFound) || !l.IgnoreRecordNotFoundError):
		l.emitSQL(ctx, fc, caller, elapsedMs, gormLogger.Error, "SQL error", logx.ErrKey, err)
	case elapsed > l.SlowThreshold && l.SlowThreshold != 0 && l.LogLevel >= gormLogger.Warn:
		l.emitSQL(ctx, fc, caller, elapsedMs, gormLogger.Warn, "slow SQL", "threshold", l.SlowThreshold.String())
	case l.LogLevel == gormLogger.Info:
		l.emitSQL(ctx, fc, caller, elapsedMs, gormLogger.Info, "SQL")
	}
}

// emitSQL 收口 Trace 三个分支重复的「traceSQL + rowsStr + 公共字段」块
// （#176）：SQL 过 redactor（#106 列级脱敏，未配置时 nil 直通）、rows == -1
// 显示 "-"、caller/elapsed_ms/rows/sql 公共字段统一在此拼装；三个分支只留
// 判级（switch）与差异字段（err / threshold）。fc 惰性取值：仅分支命中时
// 才执行（与原实现一致，Silent 判级之外零开销）。
func (l *Logger) emitSQL(ctx context.Context, fc func() (string, int64), caller string, elapsedMs float64, level gormLogger.LogLevel, msg string, extra ...any) {
	sql, rows := fc()
	fields := make([]any, 0, 8+len(extra))
	fields = append(fields,
		"caller", caller,
		"elapsed_ms", elapsedMs,
		"rows", formatRows(rows),
		"sql", l.redact.redact(sql),
	)
	fields = append(fields, extra...)

	switch level {
	case gormLogger.Error:
		l.log.CtxError(ctx, msg, fields...)
	case gormLogger.Warn:
		l.log.CtxWarn(ctx, msg, fields...)
	default:
		l.log.CtxInfo(ctx, msg, fields...)
	}
}

// formatRows 把影响行数格式化为日志值：-1（未知，非 SELECT 语句等）显示 "-"。
func formatRows(rows int64) string {
	if rows == -1 {
		return "-"
	}
	return strconv.FormatInt(rows, 10)
}
