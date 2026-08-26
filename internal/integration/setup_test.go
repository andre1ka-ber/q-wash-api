//go:build integration

// Package integration holds full-stack tests: a real Postgres (via
// testcontainers-go), every migration applied, and app.New's exact HTTP
// wiring — the same server cmd/api runs, driven only through its HTTP API.
//
// These are slow and need a working Docker daemon, so they're gated behind
// the "integration" build tag and excluded from a plain `go test ./...`.
// Run them with:
//
//	go test -tags=integration ./internal/integration/...
package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sync"
	"testing"
	"time"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
	"golang.org/x/crypto/bcrypt"
	gormpostgres "gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"q-wash-api/internal/app"
	"q-wash-api/internal/config"
	"q-wash-api/internal/platform/storage"
	"q-wash-api/internal/user"
)

// spySender is an sms.Sender that records every message instead of sending
// it, so tests can pull the OTP code (or a notification's text) straight
// out rather than needing to read stdout logs.
type spySender struct {
	mu       sync.Mutex
	messages []sentMessage
}

type sentMessage struct {
	To, Text string
}

func (s *spySender) Send(_ context.Context, to, text string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.messages = append(s.messages, sentMessage{To: to, Text: text})
	return nil
}

var otpCodeRegexp = regexp.MustCompile(`\b(\d{6})\b`)

// lastCodeFor returns the 6-digit OTP code from the most recent message
// sent to phone.
func (s *spySender) lastCodeFor(t *testing.T, phone string) string {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := len(s.messages) - 1; i >= 0; i-- {
		if s.messages[i].To != phone {
			continue
		}
		match := otpCodeRegexp.FindStringSubmatch(s.messages[i].Text)
		if match == nil {
			t.Fatalf("message to %s had no 6-digit code: %q", phone, s.messages[i].Text)
		}
		return match[1]
	}
	t.Fatalf("no message was sent to %s", phone)
	return ""
}

// testEnv bundles everything a test scenario needs: an HTTP client
// pointed at a live server, the spy sender for pulling OTP codes, and
// direct DB access for the one thing the API can't do — promoting a user
// to staff/admin (there's no such endpoint by design).
type testEnv struct {
	baseURL string
	client  *http.Client
	sms     *spySender
	db      *gorm.DB
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	ctx := context.Background()

	pgContainer, err := tcpostgres.Run(ctx,
		"postgres:16-alpine",
		tcpostgres.WithDatabase("pegasus_test"),
		tcpostgres.WithUsername("pegasus"),
		tcpostgres.WithPassword("pegasus"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(60*time.Second),
		),
	)
	if err != nil {
		t.Fatalf("start postgres container: %v", err)
	}
	t.Cleanup(func() {
		if err := pgContainer.Terminate(context.Background()); err != nil {
			t.Logf("terminate postgres container: %v", err)
		}
	})

	connStr, err := pgContainer.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("get connection string: %v", err)
	}

	m, err := migrate.New("file://../../migrations", connStr)
	if err != nil {
		t.Fatalf("create migrator: %v", err)
	}
	if err := m.Up(); err != nil && err != migrate.ErrNoChange {
		t.Fatalf("run migrations: %v", err)
	}

	database, err := gorm.Open(gormpostgres.Open(connStr), &gorm.Config{
		Logger: gormlogger.Default.LogMode(gormlogger.Silent),
	})
	if err != nil {
		t.Fatalf("connect gorm: %v", err)
	}

	cfg := config.Config{
		Env: "test",
		Auth: config.AuthConfig{
			AccessTokenSecret:  "test-access-secret",
			AccessTokenTTL:     15 * time.Minute,
			RefreshTokenPepper: "test-refresh-pepper",
			RefreshTokenTTL:    30 * 24 * time.Hour,
			OTPTTL:             5 * time.Minute,
			OTPCooldown:        60 * time.Second,
			OTPMaxAttempts:     5,
		},
	}

	cfg.Storage = config.StorageConfig{Dir: t.TempDir(), BaseURL: "/uploads"}
	fileStorage, err := storage.NewLocalDisk(cfg.Storage.Dir, cfg.Storage.BaseURL)
	if err != nil {
		t.Fatalf("create local disk storage: %v", err)
	}

	sender := &spySender{}
	server := httptest.NewServer(app.New(database, cfg, sender, fileStorage))
	t.Cleanup(server.Close)

	return &testEnv{baseURL: server.URL, client: server.Client(), sms: sender, db: database}
}

// promoteToRole directly updates a user's role in the DB. There is no API
// endpoint for this by design (see docs/DATA_MODEL.md) — a real deployment
// would do this via direct DB access too.
func (e *testEnv) promoteToRole(t *testing.T, phone string, role user.Role) {
	t.Helper()
	err := e.db.Model(&user.User{}).Where("phone_number = ?", phone).Update("role", role).Error
	if err != nil {
		t.Fatalf("promote %s to %s: %v", phone, role, err)
	}
}

// setWashingPointID directly scopes a staff/worker user to washingPointID in
// the DB — there's no API for it (same as role promotion). Needed because
// the access token's washing_point_id claim (see reqctx.AuthUser.
// OwnsWashingPoint) is baked in at login time, so callers must re-login
// (see reLogin) after calling this to get a token reflecting it.
func (e *testEnv) setWashingPointID(t *testing.T, phone, washingPointID string) {
	t.Helper()
	err := e.db.Model(&user.User{}).Where("phone_number = ?", phone).Update("washing_point_id", washingPointID).Error
	if err != nil {
		t.Fatalf("set washing_point_id for %s: %v", phone, err)
	}
}

// setPasswordCredentials directly sets username/password_hash in the DB —
// there is no API endpoint for this by design (see docs/DATA_MODEL.md), so
// a real deployment would do this the same way.
func (e *testEnv) setPasswordCredentials(t *testing.T, phone, username, password string) {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	err = e.db.Model(&user.User{}).Where("phone_number = ?", phone).Updates(map[string]any{
		"username":      username,
		"password_hash": string(hash),
	}).Error
	if err != nil {
		t.Fatalf("set credentials for %s: %v", phone, err)
	}
}

// apiResponse wraps a decoded JSON body alongside the raw status, so tests
// can assert on both without juggling *http.Response lifetimes.
type apiResponse struct {
	status int
	body   map[string]any
}

func (r apiResponse) str(field string) string {
	v, _ := r.body[field].(string)
	return v
}

func (e *testEnv) do(t *testing.T, method, path, accessToken string, body any) apiResponse {
	t.Helper()

	var reqBody *bytes.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal request body: %v", err)
		}
		reqBody = bytes.NewReader(raw)
	} else {
		reqBody = bytes.NewReader(nil)
	}

	req, err := http.NewRequest(method, e.baseURL+path, reqBody)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if accessToken != "" {
		req.Header.Set("Authorization", "Bearer "+accessToken)
	}

	resp, err := e.client.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()

	result := apiResponse{status: resp.StatusCode, body: map[string]any{}}
	if resp.ContentLength != 0 {
		if err := json.NewDecoder(resp.Body).Decode(&result.body); err != nil && resp.StatusCode != http.StatusNoContent {
			t.Fatalf("%s %s: decode response: %v", method, path, err)
		}
	}
	return result
}

// uploadFile POSTs a multipart/form-data request with one "file" part
// (named filename, containing content) and, if isCover, an "is_cover"
// field set to "true" — the shape internal/photo.Handler.upload expects.
func (e *testEnv) uploadFile(t *testing.T, path, accessToken, filename string, content []byte, isCover bool) apiResponse {
	t.Helper()

	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	part, err := writer.CreateFormFile("file", filename)
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	if _, err := part.Write(content); err != nil {
		t.Fatalf("write form file content: %v", err)
	}
	if isCover {
		if err := writer.WriteField("is_cover", "true"); err != nil {
			t.Fatalf("write is_cover field: %v", err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}

	req, err := http.NewRequest(http.MethodPost, e.baseURL+path, &buf)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	if accessToken != "" {
		req.Header.Set("Authorization", "Bearer "+accessToken)
	}

	resp, err := e.client.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	defer resp.Body.Close()

	result := apiResponse{status: resp.StatusCode, body: map[string]any{}}
	if resp.ContentLength != 0 {
		if err := json.NewDecoder(resp.Body).Decode(&result.body); err != nil && resp.StatusCode != http.StatusNoContent {
			t.Fatalf("POST %s: decode response: %v", path, err)
		}
	}
	return result
}

// loginAs runs the full OTP request/verify flow for phone and returns the
// access token. If role is non-empty, the user is promoted to that role
// (directly in the DB) before verifying, so the returned token's "role"
// claim reflects it.
func (e *testEnv) loginAs(t *testing.T, phone string, role user.Role) string {
	t.Helper()

	reqResp := e.do(t, http.MethodPost, "/api/v1/auth/otp/request", "", map[string]string{"phone_number": phone})
	if reqResp.status != http.StatusAccepted {
		t.Fatalf("otp request: expected 202, got %d (%v)", reqResp.status, reqResp.body)
	}
	code := e.sms.lastCodeFor(t, phone)

	if role != "" && role != user.RoleCustomer {
		// The user doesn't exist yet until first verify creates it, so do
		// an initial verify, then promote, then verify again to get a
		// token whose role claim matches.
		first := e.do(t, http.MethodPost, "/api/v1/auth/otp/verify", "", map[string]string{"phone_number": phone, "code": code})
		if first.status != http.StatusOK {
			t.Fatalf("otp verify (pre-promote): expected 200, got %d (%v)", first.status, first.body)
		}
		e.promoteToRole(t, phone, role)

		reqResp := e.do(t, http.MethodPost, "/api/v1/auth/otp/request", "", map[string]string{"phone_number": phone})
		if reqResp.status != http.StatusAccepted {
			t.Fatalf("otp request (post-promote): expected 202, got %d (%v)", reqResp.status, reqResp.body)
		}
		code = e.sms.lastCodeFor(t, phone)
	}

	verifyResp := e.do(t, http.MethodPost, "/api/v1/auth/otp/verify", "", map[string]string{"phone_number": phone, "code": code})
	if verifyResp.status != http.StatusOK {
		t.Fatalf("otp verify: expected 200, got %d (%v)", verifyResp.status, verifyResp.body)
	}
	token, _ := verifyResp.body["access_token"].(string)
	if token == "" {
		t.Fatalf("otp verify response had no access_token: %v", verifyResp.body)
	}
	return token
}

// reLogin re-runs the OTP request/verify flow for an already-existing user
// to get a fresh token reflecting a DB change made since their last login
// (e.g. setWashingPointID) — the same "verify again after mutating" step
// loginAs uses internally for a role promotion.
func (e *testEnv) reLogin(t *testing.T, phone string) string {
	t.Helper()

	reqResp := e.do(t, http.MethodPost, "/api/v1/auth/otp/request", "", map[string]string{"phone_number": phone})
	if reqResp.status != http.StatusAccepted {
		t.Fatalf("otp request: expected 202, got %d (%v)", reqResp.status, reqResp.body)
	}
	code := e.sms.lastCodeFor(t, phone)

	verifyResp := e.do(t, http.MethodPost, "/api/v1/auth/otp/verify", "", map[string]string{"phone_number": phone, "code": code})
	if verifyResp.status != http.StatusOK {
		t.Fatalf("otp verify: expected 200, got %d (%v)", verifyResp.status, verifyResp.body)
	}
	token, _ := verifyResp.body["access_token"].(string)
	if token == "" {
		t.Fatalf("otp verify response had no access_token: %v", verifyResp.body)
	}
	return token
}

// uniquePhone returns a syntactically valid, likely-unique E.164 number.
// Each test gets its own Postgres container, so collisions aren't a
// correctness risk, but distinct numbers still make failures easier to
// read across subtests within one test function.
func uniquePhone(seed int64) string {
	return fmt.Sprintf("+1555%07d", seed%10_000_000)
}

// futureBookingTime returns an RFC3339 UTC timestamp daysAhead from now at
// 10:00 — safely within the default washing-point hours (08:00-20:00) and
// far enough out that "must be in the future" never flakes near midnight.
func futureBookingTime(daysAhead int) string {
	day := time.Now().UTC().Truncate(24*time.Hour).AddDate(0, 0, daysAhead)
	return time.Date(day.Year(), day.Month(), day.Day(), 10, 0, 0, 0, time.UTC).Format(time.RFC3339)
}

// futureBookingDate is futureBookingTime's date-only counterpart, for
// GET .../availability?date= calls that need to line up with a
// futureBookingTime(daysAhead) booking on the same calendar day.
func futureBookingDate(daysAhead int) string {
	return time.Now().UTC().Truncate(24*time.Hour).AddDate(0, 0, daysAhead).Format("2006-01-02")
}

// todayBookingTime returns an RFC3339 timestamp minutesFromNow from real
// wall-clock now — for GET .../board, which (unlike every other endpoint
// above) always scopes to *today* server-side with no ?date= override, so
// tests exercising it can't book days ahead the way futureBookingTime does.
// Callers must give the washing point wide-open hours (e.g. "00:00"/"23:59")
// so this never trips outside_operating_hours regardless of real time of day,
// and should keep minutesFromNow small — a large offset risks crossing into
// tomorrow in businessLocation (Asia/Dushanbe) if run near local midnight,
// same real-time edge case futureBookingTime's own doc comment calls out.
func todayBookingTime(minutesFromNow int) string {
	return time.Now().UTC().Add(time.Duration(minutesFromNow) * time.Minute).Format(time.RFC3339)
}
