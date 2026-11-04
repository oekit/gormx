package gormx

import (
	"fmt"
	"time"

	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

// MySQLConfig 是 MySQL 连接配置，可直接从 YAML 反序列化。
type MySQLConfig struct {
	Host           string `yaml:"host"`
	Port           int    `yaml:"port"`
	User           string `yaml:"user"`
	Password       string `yaml:"password"`
	DBName         string `yaml:"dbname"`
	MaxIdleConns   int    `yaml:"max_idle_conns"`
	MaxOpenConns   int    `yaml:"max_open_conns"`
	ConnMaxLifeMin int    `yaml:"conn_max_life_min"`
}

// OpenMySQL 从配置创建 GORM DB 实例（含连接池配置；零 tracing 注入，
// 需要 SQL Span 经 github.com/oekit/tracing/gormotel 显式挂载）。
//
// 用法：
//
//	db, err := gormx.OpenMySQL(gormx.MySQLConfig{
//	    Host: cfg.Database.Host, Port: cfg.Database.Port,
//	    User: cfg.Database.User, Password: cfg.Database.Password,
//	    DBName: cfg.Database.DBName,
//	    MaxIdleConns: 10, MaxOpenConns: 100,
//	})
func OpenMySQL(cfg MySQLConfig) (*gorm.DB, error) {
	if cfg.MaxIdleConns <= 0 {
		cfg.MaxIdleConns = 10
	}
	if cfg.MaxOpenConns <= 0 {
		cfg.MaxOpenConns = 100
	}

	dsn := fmt.Sprintf("%s:%s@tcp(%s:%d)/%s?charset=utf8mb4&parseTime=True&loc=Local",
		cfg.User, cfg.Password, cfg.Host, cfg.Port, cfg.DBName)

	// ConnMaxLifetime 生产默认 30 分钟（防火墙/NAT 空闲连接被静默掐断后，
	// 不限期的长连接会引发 "invalid connection" 突发错误——周期重建是
	// database/sql 的标准实践）。显式传负值 = 真正不限制。
	connMaxLife := time.Duration(cfg.ConnMaxLifeMin) * time.Minute
	if connMaxLife == 0 {
		connMaxLife = 30 * time.Minute
	}

	return Open(mysql.Open(dsn), WithConnectionPool(cfg.MaxOpenConns, cfg.MaxIdleConns, connMaxLife)) // #76：maxOpen 在前
}
