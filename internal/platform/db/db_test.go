package db

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestNewLogger_Production(t *testing.T) {
	var out bytes.Buffer
	l := NewLogger(&out, logger.Warn, true)

	filter, ok := l.(gorm.ParamsFilter)
	if !ok {
		t.Fatal("logger must be able to drop query parameters")
	}
	if _, params := filter.ParamsFilter(context.Background(), "SELECT ?", "+992900000000"); len(params) != 0 {
		t.Fatalf("production must not log parameter values, got %v", params)
	}

	query := func() (string, int64) { return "SELECT 1", 0 }
	l.Trace(context.Background(), time.Now(), query, gorm.ErrRecordNotFound)
	if out.Len() != 0 {
		t.Fatalf("a not-found lookup must not be logged, got %q", out.String())
	}
	l.Trace(context.Background(), time.Now(), query, gorm.ErrInvalidDB)
	if !strings.Contains(out.String(), "SELECT 1") {
		t.Fatalf("real errors must still be logged, got %q", out.String())
	}
}

func TestNewLogger_DevelopmentKeepsParameters(t *testing.T) {
	l := NewLogger(&bytes.Buffer{}, logger.Info, false)
	_, params := l.(gorm.ParamsFilter).ParamsFilter(context.Background(), "SELECT ?", "x")
	if len(params) != 1 {
		t.Fatalf("development keeps parameter values, got %v", params)
	}
}
