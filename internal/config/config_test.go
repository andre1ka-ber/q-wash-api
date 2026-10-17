package config

import (
	"strings"
	"testing"
	"time"
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

	t.Run("production refuses the built-in dev DB password", func(t *testing.T) {
		c := production()
		c.DB.Password = devDBPassword
		if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "DB_PASSWORD") {
			t.Fatalf("expected a DB_PASSWORD error, got %v", err)
		}
		c.DB.Password = "a-real-database-password"
		if err := c.Validate(); err != nil {
			t.Fatalf("a non-default password must pass, got %v", err)
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
	c.HTTP.TrustProxyHeaders = true
	c.HTTP.CORSAllowedOrigins = []string{"*"}
	if got := c.Warnings(); len(got) != 1 || !strings.Contains(got[0], "CORS_ALLOWED_ORIGINS") {
		t.Fatalf("expected a CORS warning, got %v", got)
	}

	c.HTTP.TrustProxyHeaders = false
	c.HTTP.CORSAllowedOrigins = []string{"https://cabinet.example.com"}
	if got := c.Warnings(); len(got) != 1 || !strings.Contains(got[0], "TRUST_PROXY_HEADERS") {
		t.Fatalf("expected a proxy-trust warning in production, got %v", got)
	}
	c.HTTP.TrustProxyHeaders = true

	c.DB.SSLMode = "disable"
	if got := c.Warnings(); len(got) != 1 || !strings.Contains(got[0], "DB_SSLMODE") {
		t.Fatalf("expected an unencrypted-DB warning in production, got %v", got)
	}
	c.DB.SSLMode = "require"

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
	if _, err := Load(); err == nil {
		t.Fatal("Load must fail for production with the default DB password")
	}

	t.Setenv("DB_PASSWORD", "a-real-database-password")
	if _, err := Load(); err != nil {
		t.Fatalf("Load with real secrets: %v", err)
	}
}

func TestLoadProxyTrustAndRateLimits(t *testing.T) {
	t.Run("defaults: proxy trusted in development, not in production", func(t *testing.T) {
		cfg, err := Load()
		if err != nil || !cfg.HTTP.TrustProxyHeaders {
			t.Fatalf("development: trust=%v err=%v", cfg.HTTP.TrustProxyHeaders, err)
		}

		t.Setenv("APP_ENV", "production")
		t.Setenv("JWT_ACCESS_SECRET", goodSecret)
		t.Setenv("REFRESH_TOKEN_PEPPER", goodSecret)
		t.Setenv("DB_PASSWORD", "a-real-database-password")
		cfg, err = Load()
		if err != nil || cfg.HTTP.TrustProxyHeaders {
			t.Fatalf("production: trust=%v err=%v", cfg.HTTP.TrustProxyHeaders, err)
		}

		t.Setenv("TRUST_PROXY_HEADERS", "true")
		cfg, err = Load()
		if err != nil || !cfg.HTTP.TrustProxyHeaders {
			t.Fatalf("explicit true: trust=%v err=%v", cfg.HTTP.TrustProxyHeaders, err)
		}
	})

	t.Run("an invalid boolean is an error", func(t *testing.T) {
		t.Setenv("TRUST_PROXY_HEADERS", "maybe")
		if _, err := Load(); err == nil {
			t.Fatal("expected an error")
		}
	})

	t.Run("rate-limit defaults", func(t *testing.T) {
		cfg, err := Load()
		if err != nil {
			t.Fatal(err)
		}
		want := RateLimitConfig{LoginPerMinute: 10, LoginMaxFailures: 5, LoginLockout: 15 * time.Minute, OTPRequestPerHour: 5, OTPVerifyPerHour: 20, QRScanPerMinute: 60}
		if cfg.RateLimit != want {
			t.Fatalf("got %+v, want %+v", cfg.RateLimit, want)
		}
	})

	t.Run("0 turns a limit off, negative is refused", func(t *testing.T) {
		t.Setenv("RATE_LIMIT_QR_SCAN_PER_MINUTE", "0")
		cfg, err := Load()
		if err != nil || cfg.RateLimit.QRScanPerMinute != 0 {
			t.Fatalf("0: got %+v err=%v", cfg.RateLimit, err)
		}
		t.Setenv("RATE_LIMIT_QR_SCAN_PER_MINUTE", "-1")
		if _, err := Load(); err == nil {
			t.Fatal("negative limit must be refused")
		}
	})
}

func TestLoadStaffAccessTokenTTL(t *testing.T) {
	cfg, err := Load()
	if err != nil || cfg.Auth.StaffAccessTokenTTL != 5*time.Minute || cfg.Auth.AccessTokenTTL != 15*time.Minute {
		t.Fatalf("defaults: staff=%v access=%v err=%v", cfg.Auth.StaffAccessTokenTTL, cfg.Auth.AccessTokenTTL, err)
	}

	t.Setenv("JWT_STAFF_ACCESS_TTL", "2m")
	if cfg, err = Load(); err != nil || cfg.Auth.StaffAccessTokenTTL != 2*time.Minute {
		t.Fatalf("override: staff=%v err=%v", cfg.Auth.StaffAccessTokenTTL, err)
	}

	t.Setenv("JWT_STAFF_ACCESS_TTL", "soon")
	if _, err = Load(); err == nil {
		t.Fatal("an unparsable duration must fail startup")
	}
}

func TestLoadMonitoring(t *testing.T) {
	t.Run("empty SENTRY_DSN by default", func(t *testing.T) {
		cfg, err := Load()
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Monitoring.DSN != "" {
			t.Fatalf("expected empty DSN by default, got %q", cfg.Monitoring.DSN)
		}
	})

	t.Run("reads SENTRY_DSN when set", func(t *testing.T) {
		t.Setenv("SENTRY_DSN", "https://key@sentry.example/1")
		cfg, err := Load()
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Monitoring.DSN != "https://key@sentry.example/1" {
			t.Fatalf("expected DSN to be read from env, got %q", cfg.Monitoring.DSN)
		}
	})
}
