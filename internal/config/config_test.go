package config

import (
	"strings"
	"testing"
)

const goodSecret = "0123456789abcdef0123456789abcdef" // 32 chars

func production() Config {
	return Config{
		Env:  "production",
		Auth: AuthConfig{AccessTokenSecret: goodSecret, RefreshTokenPepper: goodSecret + "x"},
	}
}

func TestValidate(t *testing.T) {
	t.Run("a properly configured production passes", func(t *testing.T) {
		if err := production().Validate(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("production refuses the built-in dev secrets", func(t *testing.T) {
		c := production()
		c.Auth.AccessTokenSecret = devAccessSecret
		c.Auth.RefreshTokenPepper = devRefreshPepper
		err := c.Validate()
		if err == nil || !strings.Contains(err.Error(), "JWT_ACCESS_SECRET") || !strings.Contains(err.Error(), "REFRESH_TOKEN_PEPPER") {
			t.Fatalf("expected both secrets to be reported, got %v", err)
		}
	})

	t.Run("production refuses secrets that are too short", func(t *testing.T) {
		c := production()
		c.Auth.AccessTokenSecret = "short"
		if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "too short") {
			t.Fatalf("expected a too-short error, got %v", err)
		}
	})

	t.Run("development and test are never blocked", func(t *testing.T) {
		for _, env := range []string{"development", "test", ""} {
			c := Config{Env: env, Auth: AuthConfig{AccessTokenSecret: devAccessSecret, RefreshTokenPepper: devRefreshPepper}}
			if err := c.Validate(); err != nil {
				t.Errorf("env %q: %v", env, err)
			}
		}
	})
}

func TestWarnings(t *testing.T) {
	c := production()
	c.HTTP.CORSAllowedOrigins = []string{"*"}
	if got := c.Warnings(); len(got) != 1 || !strings.Contains(got[0], "CORS_ALLOWED_ORIGINS") {
		t.Fatalf("expected a CORS warning, got %v", got)
	}

	c.HTTP.CORSAllowedOrigins = []string{"https://cabinet.example.com"}
	if got := c.Warnings(); len(got) != 0 {
		t.Fatalf("locked-down origins should not warn, got %v", got)
	}

	dev := Config{Env: "development", HTTP: HTTPConfig{CORSAllowedOrigins: []string{"*"}}}
	if got := dev.Warnings(); len(got) != 0 {
		t.Fatalf("CORS * is fine outside production, got %v", got)
	}
}

func TestLoadAppliesValidation(t *testing.T) {
	t.Setenv("APP_ENV", "production")
	t.Setenv("JWT_ACCESS_SECRET", "")
	t.Setenv("REFRESH_TOKEN_PEPPER", "")
	if _, err := Load(); err == nil {
		t.Fatal("Load must fail for production with the dev secrets")
	}

	t.Setenv("JWT_ACCESS_SECRET", goodSecret)
	t.Setenv("REFRESH_TOKEN_PEPPER", goodSecret)
	if _, err := Load(); err != nil {
		t.Fatalf("Load with real secrets: %v", err)
	}
}
