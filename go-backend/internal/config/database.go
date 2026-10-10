package config

import (
	"log"
	"os"
	"time"

	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// InitDB 初始化数据库连接
func InitDB(cfg *DatabaseConfig) (*gorm.DB, error) {
	db, err := gorm.Open(mysql.Open(cfg.DSN()), &gorm.Config{
		Logger: logger.New(log.New(os.Stdout, "", log.LstdFlags), logger.Config{
			LogLevel: logger.Warn, SlowThreshold: time.Second,
			ParameterizedQueries: true, IgnoreRecordNotFoundError: true,
		}),
	})
	if err != nil {
		return nil, err
	}

	sqlDB, err := db.DB()
	if err != nil {
		return nil, err
	}

	// 连接池配置
	sqlDB.SetMaxIdleConns(10)
	sqlDB.SetMaxOpenConns(100)

	log.Println("[DB] MySQL connected successfully")
	return db, nil
}
