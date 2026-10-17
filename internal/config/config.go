// Package config loads application configuration from environment variables.
package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Env        string
	HTTP       HTTPConfig
	DB         DBConfig
	Auth       AuthConfig
	Storage    StorageConfig
	Push       PushConfig
	SMS        SMSConfig
	Monitoring MonitoringConfig
	RateLimit  RateLimitConfig
}

// RateLimitConfig sets the abuse limits on the unauthenticated endpoints.
// Per-IP limits key on the client IP, which is only meaningful when
// HTTPConfig.TrustProxyHeaders matches the deployment (see its comment). A
// value of 0 turns that limit off; the zero RateLimitConfig (used by tests)
// therefore limits nothing, while Load always fills in the defaults below.
type RateLimitConfig struct {
	LoginPerMinute    int           // POST /auth/login, per IP (default 10)
	LoginMaxFailures  int           // wrong passwords per username before lockout (default 5)
	LoginLockout      time.Duration // how long a username stays locked (default 15m)
	OTPRequestPerHour int           // POST /auth/otp/request, per IP (default 5)
	OTPVerifyPerHour  int           // POST /auth/otp/verify, per IP (default 20)
	QRScanPerMinute   int           // GET /q/{token} and /qr-codes/scan/{token}, per IP (default 60)
}

type HTTPConfig struct {
	Port string
	// TrustProxyHeaders makes the API take the client IP from the
	// X-Forwarded-For / X-Real-IP headers. Turn it on only when the API sits
	// behind a reverse proxy that overwrites those headers; otherwise any
	// client can spoof its IP and dodge the per-IP rate limits. Off, the IP is
	// the TCP peer — behind a proxy that is the proxy's address, so all users
	// would then share one rate-limit bucket. Defaults to on outside
	// production and off in production, so a production deploy behind a proxy
	// must set TRUST_PROXY_HEADERS=true explicitly.
	TrustProxyHeaders bool
	// CORSAllowedOrigins lets a separately-hosted frontend (different
	// origin/port) call this API from a browser. Comma-separated in env;
	// "*" (the default) is fine for local dev but should be locked down to
	// specific origins in any real deployment.
	CORSAllowedOrigins []string
}

type DBConfig struct {
	Host     string
	Port     string
	User     string
	Password string
	Name     string
	SSLMode  string
}

func (c DBConfig) DSN() string {
	return fmt.Sprintf(
		"host=%s port=%s user=%s password=%s dbname=%s sslmode=%s",
		c.Host, c.Port, c.User, c.Password, c.Name, c.SSLMode,
	)
}

type AuthConfig struct {
	// AccessTokenSecret signs short-lived JWT access tokens.
	AccessTokenSecret string
	AccessTokenTTL    time.Duration

	// RefreshTokenPepper is an HMAC key used to hash opaque refresh tokens
	// before storing/looking them up, so a leaked DB alone isn't enough to
	// forge a valid refresh token. Refresh tokens themselves are NOT JWTs.
	RefreshTokenPepper string
	RefreshTokenTTL    time.Duration

	OTPTTL         time.Duration
	OTPCooldown    time.Duration // minimum time between two OTP requests for the same phone
	OTPMaxAttempts int
}

// StorageConfig configures where uploaded photos (docs/PLAN_WEB_APPS.md
// phase 4) are written and served from. See
// internal/platform/storage.LocalDisk — dev-only, not multi-instance safe.
type StorageConfig struct {
	Dir     string
	BaseURL string
}

// PushConfig configures Firebase Cloud Messaging. Both fields empty means
// push is disabled (a no-op sender is used). The service-account key comes
// either inline (FCM_SERVICE_ACCOUNT_JSON) or from a file
// (FCM_SERVICE_ACCOUNT_FILE) — never commit it.
type PushConfig struct {
	ProjectID          string
	ServiceAccountJSON []byte
}

// SMSConfig selects the OTP delivery provider. Provider "stub" (the default)
// only logs the code; "payom" sends through Payom.tj and requires the Payom*
// fields (see internal/platform/sms.PayomConfig). The token is a secret —
// never commit it.
type SMSConfig struct {
	Provider           string
	PayomBaseURL       string
	PayomToken         string
	PayomSenderName    string
	PayomOTPTemplateID string
}

// MonitoringConfig configures Sentry error reporting. Empty DSN means
// disabled (no-op), same shape as PushConfig. Only ever enabled in
// production, regardless of whether a DSN happens to be set (see
// internal/platform/monitoring).
type MonitoringConfig struct {
	DSN string
}

// Dev-only fallbacks for the two signing secrets: convenient locally, fatal
// if they ever reach production (anyone could then forge access tokens).
const (
	devAccessSecret  = "dev-access-secret-change-me"
	devRefreshPepper = "dev-refresh-pepper-change-me"
	minSecretLen     = 32
)

// IsProduction reports whether the API is running as a production deploy.
func (c Config) IsProduction() bool { return c.Env == "production" }

// Validate refuses to start a production deploy with unsafe configuration:
// the built-in dev secrets, or secrets too short to be real. Other
// environments (development, test) are never blocked. CORS "*" is not a
// startup error (a deploy may legitimately serve a public API) but is
// reported by Warnings.
func (c Config) Validate() error {
	if err := c.RateLimit.validate(); err != nil {
		return err
	}
	if !c.IsProduction() {
		return nil
	}
	var problems []string
	check := func(name, value, devDefault string) {
		switch {
		case value == devDefault:
			problems = append(problems, name+" is still the built-in dev default")
		case len(value) < minSecretLen:
			problems = append(problems, fmt.Sprintf("%s is too short (need at least %d characters)", name, minSecretLen))
		}
	}
	check("JWT_ACCESS_SECRET", c.Auth.AccessTokenSecret, devAccessSecret)
	check("REFRESH_TOKEN_PEPPER", c.Auth.RefreshTokenPepper, devRefreshPepper)
	if len(problems) > 0 {
		return fmt.Errorf("unsafe production configuration: %s", strings.Join(problems, "; "))
	}
	return nil
}

// validate rejects negative limits; 0 means "off" and is allowed.
func (r RateLimitConfig) validate() error {
	if r.LoginPerMinute < 0 || r.LoginMaxFailures < 0 || r.LoginLockout < 0 ||
		r.OTPRequestPerHour < 0 || r.OTPVerifyPerHour < 0 || r.QRScanPerMinute < 0 {
		return errors.New("RATE_LIMIT_* values must not be negative (0 turns a limit off)")
	}
	return nil
}

// Warnings lists non-fatal configuration smells worth logging at startup.
func (c Config) Warnings() []string {
	var warnings []string
	if c.IsProduction() {
		if !c.HTTP.TrustProxyHeaders {
			warnings = append(warnings, "TRUST_PROXY_HEADERS is off in production: client IPs are the TCP peer's, so behind a reverse proxy every user shares one rate-limit bucket; set TRUST_PROXY_HEADERS=true if (and only if) your proxy overwrites X-Forwarded-For")
		}
		for _, origin := range c.HTTP.CORSAllowedOrigins {
			if origin == "*" {
				warnings = append(warnings, "CORS_ALLOWED_ORIGINS is \"*\" in production; lock it down to the real frontend origins")
				break
			}
		}
	}
	return warnings
}

func Load() (Config, error) {
	cfg := Config{
		Env: getEnv("APP_ENV", "development"),
		HTTP: HTTPConfig{
			Port:               getEnv("HTTP_PORT", "8080"),
			CORSAllowedOrigins: getCSV("CORS_ALLOWED_ORIGINS", []string{"*"}),
		},
		DB: DBConfig{
			Host:     getEnv("DB_HOST", "localhost"),
			Port:     getEnv("DB_PORT", "5432"),
			User:     getEnv("DB_USER", "pegasus"),
			Password: getEnv("DB_PASSWORD", "pegasus"),
			Name:     getEnv("DB_NAME", "pegasus"),
			SSLMode:  getEnv("DB_SSLMODE", "disable"),
		},
		Auth: AuthConfig{
			AccessTokenSecret:  getEnv("JWT_ACCESS_SECRET", devAccessSecret),
			RefreshTokenPepper: getEnv("REFRESH_TOKEN_PEPPER", devRefreshPepper),
		},
		Storage: StorageConfig{
			Dir:     getEnv("UPLOADS_DIR", "./uploads"),
			BaseURL: getEnv("UPLOADS_BASE_URL", "/uploads"),
		},
		Push: PushConfig{ProjectID: getEnv("FCM_PROJECT_ID", "")},
		SMS: SMSConfig{
			Provider:           getEnv("SMS_PROVIDER", "stub"),
			PayomBaseURL:       getEnv("PAYOM_BASE_URL", ""),
			PayomToken:         getEnv("PAYOM_API_TOKEN", ""),
			PayomSenderName:    getEnv("PAYOM_SENDER_NAME", "QWash"),
			PayomOTPTemplateID: getEnv("PAYOM_OTP_TEMPLATE_ID", ""),
		},
		Monitoring: MonitoringConfig{DSN: getEnv("SENTRY_DSN", "")},
	}

	var err error
	if inline := getEnv("FCM_SERVICE_ACCOUNT_JSON", ""); inline != "" {
		cfg.Push.ServiceAccountJSON = []byte(inline)
	} else if path := getEnv("FCM_SERVICE_ACCOUNT_FILE", ""); path != "" {
		if cfg.Push.ServiceAccountJSON, err = os.ReadFile(path); err != nil {
			return Config{}, fmt.Errorf("read FCM_SERVICE_ACCOUNT_FILE: %w", err)
		}
	}
	if cfg.Auth.AccessTokenTTL, err = getDuration("JWT_ACCESS_TTL", 15*time.Minute); err != nil {
		return Config{}, err
	}
	if cfg.Auth.RefreshTokenTTL, err = getDuration("JWT_REFRESH_TTL", 30*24*time.Hour); err != nil {
		return Config{}, err
	}
	if cfg.Auth.OTPTTL, err = getDuration("OTP_TTL", 5*time.Minute); err != nil {
		return Config{}, err
	}
	if cfg.Auth.OTPCooldown, err = getDuration("OTP_COOLDOWN", 60*time.Second); err != nil {
		return Config{}, err
	}
	if cfg.Auth.OTPMaxAttempts, err = getInt("OTP_MAX_ATTEMPTS", 5); err != nil {
		return Config{}, err
	}
	if cfg.HTTP.TrustProxyHeaders, err = getBool("TRUST_PROXY_HEADERS", !cfg.IsProduction()); err != nil {
		return Config{}, err
	}
	rl := &cfg.RateLimit
	for _, f := range []struct {
		dst      *int
		env      string
		fallback int
	}{
		{&rl.LoginPerMinute, "RATE_LIMIT_LOGIN_PER_MINUTE", 10},
		{&rl.LoginMaxFailures, "RATE_LIMIT_LOGIN_MAX_FAILURES", 5},
		{&rl.OTPRequestPerHour, "RATE_LIMIT_OTP_REQUEST_PER_HOUR", 5},
		{&rl.OTPVerifyPerHour, "RATE_LIMIT_OTP_VERIFY_PER_HOUR", 20},
		{&rl.QRScanPerMinute, "RATE_LIMIT_QR_SCAN_PER_MINUTE", 60},
	} {
		if *f.dst, err = getInt(f.env, f.fallback); err != nil {
			return Config{}, err
		}
	}
	if rl.LoginLockout, err = getDuration("RATE_LIMIT_LOGIN_LOCKOUT", 15*time.Minute); err != nil {
		return Config{}, err
	}

	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func getEnv(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return fallback
}

func getDuration(key string, fallback time.Duration) (time.Duration, error) {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return fallback, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("invalid duration for %s: %w", key, err)
	}
	return d, nil
}

func getBool(key string, fallback bool) (bool, error) {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return fallback, nil
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return false, fmt.Errorf("invalid boolean for %s: %w", key, err)
	}
	return b, nil
}

func getCSV(key string, fallback []string) []string {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return fallback
	}
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return fallback
	}
	return out
}

func getInt(key string, fallback int) (int, error) {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return fallback, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("invalid integer for %s: %w", key, err)
	}
	return n, nil
}
