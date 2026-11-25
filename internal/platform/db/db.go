// Package db wires up the GORM database connection.
package db

import (
	"fmt"
	"io"
	"log"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"q-wash-api/internal/config"
)

// NewLogger builds the query logger. In production it logs placeholders
// instead of values (queries carry phone numbers, usernames and token hashes)
// and skips "record not found", which is an ordinary lookup result.
func NewLogger(out io.Writer, level logger.LogLevel, production bool) logger.Interface {
	return logger.New(log.New(out, "\r\n", log.LstdFlags), logger.Config{
		SlowThreshold:             200 * time.Millisecond,
		LogLevel:                  level,
		IgnoreRecordNotFoundError: production,
		ParameterizedQueries:      production,
	})
}

func Connect(cfg config.DBConfig, gormLogger logger.Interface) (*gorm.DB, error) {
	db, err := gorm.Open(postgres.Open(cfg.DSN()), &gorm.Config{
		Logger: gormLogger,
	})
	if err != nil {
		return nil, fmt.Errorf("connect to postgres: %w", err)
	}

	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("get underlying sql.DB: %w", err)
	}
	if err := sqlDB.Ping(); err != nil {
		return nil, fmt.Errorf("ping postgres: %w", err)
	}

	return db, nil
}
