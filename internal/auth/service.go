package auth

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"

	"q-wash-api/internal/apperror"
	"q-wash-api/internal/config"
	"q-wash-api/internal/platform/jwt"
	"q-wash-api/internal/platform/sms"
	"q-wash-api/internal/user"
)

var (
	phoneRegexp   = regexp.MustCompile(`^\+[1-9]\d{6,14}$`)
	otpCodeRegexp = regexp.MustCompile(`^\d{6}$`)
)

func ValidatePhoneNumber(phone string) error {
	if !phoneRegexp.MatchString(phone) {
		return apperror.BadRequest("invalid_phone_number", "phone number must be in E.164 format, e.g. +15551234567")
	}
	return nil
}

// DefaultCountryCode is prepended to phone numbers entered without one
// (this app only serves Tajikistan, +992 — same single-market assumption
// as businessLocation in internal/queue).
const DefaultCountryCode = "992"

// NormalizePhoneNumber turns staff-typed input into E.164: spaces, dashes
// and parentheses are dropped; "+..." is kept as given; "00..." becomes
// "+..."; a number that already starts with the country code gets the "+";
// a bare local number (with or without a leading 0) gets DefaultCountryCode.
// The result is validated with ValidatePhoneNumber.
func NormalizePhoneNumber(raw string) (string, error) {
	var digits strings.Builder
	explicitPlus := false
	for i, r := range strings.TrimSpace(raw) {
		switch {
		case r == '+' && i == 0:
			explicitPlus = true
		case r >= '0' && r <= '9':
			digits.WriteRune(r)
		case r == ' ' || r == '-' || r == '(' || r == ')' || r == '.':
		default:
			return "", ValidatePhoneNumber(raw)
		}
	}
	d := digits.String()
	switch {
	case explicitPlus:
	case strings.HasPrefix(d, "00"):
		d = d[2:]
	case strings.HasPrefix(d, DefaultCountryCode):
	default:
		d = DefaultCountryCode + strings.TrimPrefix(d, "0")
	}
	phone := "+" + d
	if err := ValidatePhoneNumber(phone); err != nil {
		return "", err
	}
	return phone, nil
}

type Service struct {
	repo       *Repository
	userRepo   *user.Repository
	sms        sms.Sender
	jwtManager *jwt.Manager
	cfg        config.AuthConfig
}

func NewService(repo *Repository, userRepo *user.Repository, smsSender sms.Sender, jwtManager *jwt.Manager, cfg config.AuthConfig) *Service {
	return &Service{repo: repo, userRepo: userRepo, sms: smsSender, jwtManager: jwtManager, cfg: cfg}
}

// RequestOTP generates a one-time code, stores its hash, and "sends" it via
// the configured sms.Sender (a stdout stub in dev). It's rate-limited per
// phone number by cfg.OTPCooldown.
func (s *Service) RequestOTP(ctx context.Context, phone string) error {
	if err := ValidatePhoneNumber(phone); err != nil {
		return err
	}

	latest, err := s.repo.LatestActiveOTPCode(ctx, phone)
	if err != nil {
		return err
	}
	if latest != nil && time.Since(latest.CreatedAt) < s.cfg.OTPCooldown {
		return apperror.TooManyRequests("otp_cooldown", "please wait before requesting another code")
	}

	code, err := generateOTPCode()
	if err != nil {
		return apperror.Internal(err)
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(code), bcrypt.DefaultCost)
	if err != nil {
		return apperror.Internal(err)
	}

	otp := &OTPCode{
		PhoneNumber: phone,
		CodeHash:    string(hash),
		ExpiresAt:   time.Now().Add(s.cfg.OTPTTL),
	}
	if err := s.repo.CreateOTPCode(ctx, otp); err != nil {
		return err
	}

	message := fmt.Sprintf("Your Pegasus Wash verification code is %s. It expires in %d minutes.", code, int(s.cfg.OTPTTL.Minutes()))
	if err := s.sms.Send(ctx, phone, message); err != nil {
		return apperror.Internal(err)
	}
	return nil
}

type TokenPair struct {
	AccessToken           string
	AccessTokenExpiresAt  time.Time
	RefreshToken          string
	RefreshTokenExpiresAt time.Time
	User                  *user.User
}

// VerifyOTP validates the code, finds-or-creates the user for that phone
// number, and issues a new access/refresh token pair.
func (s *Service) VerifyOTP(ctx context.Context, phone, code string) (*TokenPair, error) {
	if err := ValidatePhoneNumber(phone); err != nil {
		return nil, err
	}
	if !otpCodeRegexp.MatchString(code) {
		return nil, apperror.BadRequest("invalid_code", "code must be 6 digits")
	}

	otp, err := s.repo.LatestActiveOTPCode(ctx, phone)
	if err != nil {
		return nil, err
	}
	if otp == nil {
		return nil, apperror.BadRequest("otp_invalid", "no active code for this phone number, request a new one")
	}
	if time.Now().After(otp.ExpiresAt) {
		return nil, apperror.BadRequest("otp_expired", "code has expired, request a new one")
	}
	if otp.Attempts >= s.cfg.OTPMaxAttempts {
		return nil, apperror.TooManyRequests("otp_locked", "too many incorrect attempts, request a new code")
	}

	otp.Attempts++
	correct := bcrypt.CompareHashAndPassword([]byte(otp.CodeHash), []byte(code)) == nil
	if !correct {
		if err := s.repo.SaveOTPCode(ctx, otp); err != nil {
			return nil, err
		}
		return nil, apperror.BadRequest("otp_invalid", "incorrect code")
	}

	now := time.Now()
	otp.ConsumedAt = &now
	if err := s.repo.SaveOTPCode(ctx, otp); err != nil {
		return nil, err
	}

	u, err := s.findOrCreateUser(ctx, phone)
	if err != nil {
		return nil, err
	}
	if err := s.userRepo.UpdateLastLogin(ctx, u.ID, now); err != nil {
		return nil, err
	}
	u.LastLoginAt = &now

	return s.issueTokenPair(ctx, u)
}

func (s *Service) findOrCreateUser(ctx context.Context, phone string) (*user.User, error) {
	u, err := s.userRepo.FindByPhone(ctx, phone)
	if err == nil {
		return u, nil
	}

	var appErr *apperror.Error
	if !errors.As(err, &appErr) || appErr.Code != "user_not_found" {
		return nil, err
	}

	newUser := &user.User{PhoneNumber: phone, Role: user.RoleCustomer}
	if err := s.userRepo.Create(ctx, newUser); err != nil {
		return nil, err
	}
	return newUser, nil
}

// LoginWithPassword authenticates a staff/admin account by username +
// password — the customer-facing flow stays phone+OTP only. Errors are
// deliberately generic (invalid_credentials) for both "no such username"
// and "wrong password", so a client can't distinguish a nonexistent
// username from a wrong password (avoids username enumeration).
func (s *Service) LoginWithPassword(ctx context.Context, username, password string) (*TokenPair, error) {
	invalidCredentials := apperror.Unauthorized("invalid_credentials", "invalid username or password")

	u, err := s.userRepo.FindByUsername(ctx, username)
	if err != nil {
		var appErr *apperror.Error
		if errors.As(err, &appErr) && appErr.Code == "user_not_found" {
			return nil, invalidCredentials
		}
		return nil, err
	}

	if u.PasswordHash == nil {
		return nil, invalidCredentials
	}
	if bcrypt.CompareHashAndPassword([]byte(*u.PasswordHash), []byte(password)) != nil {
		return nil, invalidCredentials
	}

	if u.Role != user.RoleStaff && u.Role != user.RoleAdmin && u.Role != user.RoleWorker {
		return nil, apperror.Forbidden("forbidden", "username/password login is only available to staff, worker, and admin accounts")
	}

	now := time.Now()
	if err := s.userRepo.UpdateLastLogin(ctx, u.ID, now); err != nil {
		return nil, err
	}
	u.LastLoginAt = &now

	return s.issueTokenPair(ctx, u)
}

// Refresh rotates a refresh token: the presented token is revoked and a new
// access/refresh pair is issued, so a stolen-and-reused old token is a
// detectable signal (it will already be revoked).
func (s *Service) Refresh(ctx context.Context, refreshToken string) (*TokenPair, error) {
	hash := s.hashRefreshToken(refreshToken)
	existing, err := s.repo.FindActiveRefreshTokenByHash(ctx, hash)
	if err != nil {
		return nil, err
	}
	if existing == nil {
		return nil, apperror.Unauthorized("invalid_refresh_token", "refresh token is invalid or expired")
	}
	if err := s.repo.RevokeRefreshToken(ctx, existing.ID); err != nil {
		return nil, err
	}

	u, err := s.userRepo.FindByID(ctx, existing.UserID)
	if err != nil {
		return nil, err
	}
	return s.issueTokenPair(ctx, u)
}

// Logout revokes the given refresh token, or every active refresh token for
// the user if none is given. It's idempotent: revoking an already-invalid
// or foreign token is a no-op, not an error.
func (s *Service) Logout(ctx context.Context, userID uuid.UUID, refreshToken string) error {
	if refreshToken == "" {
		return s.repo.RevokeAllRefreshTokensForUser(ctx, userID)
	}

	hash := s.hashRefreshToken(refreshToken)
	existing, err := s.repo.FindActiveRefreshTokenByHash(ctx, hash)
	if err != nil {
		return err
	}
	if existing == nil || existing.UserID != userID {
		return nil
	}
	return s.repo.RevokeRefreshToken(ctx, existing.ID)
}

func (s *Service) issueTokenPair(ctx context.Context, u *user.User) (*TokenPair, error) {
	var washingPointID *string
	if u.WashingPointID != nil {
		id := u.WashingPointID.String()
		washingPointID = &id
	}

	accessToken, accessExpiresAt, err := s.jwtManager.Generate(u.ID.String(), string(u.Role), washingPointID)
	if err != nil {
		return nil, apperror.Internal(err)
	}

	refreshToken, err := generateRefreshToken()
	if err != nil {
		return nil, apperror.Internal(err)
	}
	refreshExpiresAt := time.Now().Add(s.cfg.RefreshTokenTTL)

	rt := &RefreshToken{
		UserID:    u.ID,
		TokenHash: s.hashRefreshToken(refreshToken),
		ExpiresAt: refreshExpiresAt,
	}
	if err := s.repo.CreateRefreshToken(ctx, rt); err != nil {
		return nil, err
	}

	return &TokenPair{
		AccessToken:           accessToken,
		AccessTokenExpiresAt:  accessExpiresAt,
		RefreshToken:          refreshToken,
		RefreshTokenExpiresAt: refreshExpiresAt,
		User:                  u,
	}, nil
}

func (s *Service) hashRefreshToken(token string) string {
	mac := hmac.New(sha256.New, []byte(s.cfg.RefreshTokenPepper))
	mac.Write([]byte(token))
	return hex.EncodeToString(mac.Sum(nil))
}

func generateOTPCode() (string, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(1_000_000))
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%06d", n.Int64()), nil
}

func generateRefreshToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
