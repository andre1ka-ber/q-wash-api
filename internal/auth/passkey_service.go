package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/google/uuid"

	"q-wash-api/internal/apperror"
	"q-wash-api/internal/config"
	"q-wash-api/internal/user"
)

const (
	passkeyChallengeTTL = 5 * time.Minute
	maxPasskeysPerUser  = 10
	passkeyRPName       = "Q Wash"
)

// passkeyProviders names the password managers behind well-known AAGUIDs, for
// the key list. Others are shown without a provider.
var passkeyProviders = map[string]string{
	"fbfc3007-154e-4ecc-8c0b-6e020557d7bd": "iCloud Keychain",
	"dd4ec289-e01d-41c9-bb89-70fa845d4bf2": "iCloud Keychain",
	"ea9b8d66-4d01-1d21-3ce4-b6b48cb575d4": "Google Password Manager",
	"53414d53-554e-4700-0000-000000000000": "Samsung Pass",
}

func passkeyProvider(aaguid []byte) string {
	id, err := uuid.FromBytes(aaguid)
	if err != nil {
		return ""
	}
	return passkeyProviders[id.String()]
}

func errPasskeysDisabled() error {
	return apperror.NotFound("passkeys_disabled", "passkey sign-in is not available")
}

func errPasskeyLimit() error {
	return apperror.Conflict("passkey_limit", fmt.Sprintf("an account can have at most %d passkeys", maxPasskeysPerUser))
}

func errPasskeyChallenge() error {
	return apperror.BadRequest("passkey_challenge_invalid", "the passkey request expired, start again")
}

// errPasskeyInvalid keeps the library's reason as the cause, for the log only.
func errPasskeyInvalid(cause error) error {
	return &apperror.Error{Status: http.StatusBadRequest, Code: "passkey_invalid", Message: "the passkey could not be verified", Err: cause}
}

// PasskeyService runs the passkey ceremonies for customer accounts. SMS-code
// login stays available next to it.
type PasskeyService struct {
	auth     *Service
	repo     *Repository
	userRepo *user.Repository
	web      *webauthn.WebAuthn // nil when passkeys are off
}

// NewPasskeyService returns a service that answers passkeys_disabled when cfg
// is empty. cfg must already have passed config validation.
func NewPasskeyService(authService *Service, repo *Repository, userRepo *user.Repository, cfg config.PasskeyConfig) (*PasskeyService, error) {
	s := &PasskeyService{auth: authService, repo: repo, userRepo: userRepo}
	if !cfg.Enabled() {
		return s, nil
	}
	androidOrigins, err := cfg.AndroidOrigins()
	if err != nil {
		return nil, err
	}
	s.web, err = webauthn.New(&webauthn.Config{
		RPID:            cfg.RPID,
		RPDisplayName:   passkeyRPName,
		RPOrigins:       []string{cfg.IOSOrigin()},
		RPOpaqueOrigins: androidOrigins,
		AuthenticatorSelection: protocol.AuthenticatorSelection{
			ResidentKey:        protocol.ResidentKeyRequirementRequired,
			RequireResidentKey: protocol.ResidentKeyRequired(),
			UserVerification:   protocol.VerificationRequired,
		},
		AttestationPreference: protocol.PreferNoAttestation,
		// No extension output is used, and some password managers send them
		// unasked.
		ExtensionsUnsolicitedOutputPolicy: protocol.UnsolicitedOutputPolicyIgnore,
	})
	if err != nil {
		return nil, fmt.Errorf("configure passkeys: %w", err)
	}
	return s, nil
}

// webAuthnUser adapts a customer and their passkeys to the library. The user
// handle is the account's id, which carries no personal data.
type webAuthnUser struct {
	user     *user.User
	passkeys []Passkey
}

func (u webAuthnUser) WebAuthnID() []byte { return u.user.ID[:] }

func (u webAuthnUser) WebAuthnName() string { return u.user.PhoneOrEmpty() }

func (u webAuthnUser) WebAuthnDisplayName() string {
	if u.user.Name != nil && *u.user.Name != "" {
		return *u.user.Name
	}
	return u.user.PhoneOrEmpty()
}

func (u webAuthnUser) WebAuthnCredentials() []webauthn.Credential {
	credentials := make([]webauthn.Credential, len(u.passkeys))
	for i, p := range u.passkeys {
		credentials[i] = p.Credential
	}
	return credentials
}

func (s *PasskeyService) customer(ctx context.Context, userID uuid.UUID) (webAuthnUser, error) {
	u, err := s.userRepo.FindByID(ctx, userID)
	if err != nil {
		return webAuthnUser{}, err
	}
	if u.Role != user.RoleCustomer {
		return webAuthnUser{}, apperror.Forbidden("forbidden", "passkeys are only available to customer accounts")
	}
	passkeys, err := s.repo.ListPasskeys(ctx, u.ID)
	if err != nil {
		return webAuthnUser{}, err
	}
	return webAuthnUser{user: u, passkeys: passkeys}, nil
}

func (s *PasskeyService) saveChallenge(ctx context.Context, userID *uuid.UUID, purpose challengePurpose, session *webauthn.SessionData) (uuid.UUID, error) {
	now := s.auth.now()
	c := &PasskeyChallenge{UserID: userID, Purpose: purpose, Session: *session, ExpiresAt: now.Add(passkeyChallengeTTL)}
	if err := s.repo.CreateChallenge(ctx, c, now); err != nil {
		return uuid.Nil, err
	}
	return c.ID, nil
}

func (s *PasskeyService) takeChallenge(ctx context.Context, id uuid.UUID, purpose challengePurpose) (*PasskeyChallenge, error) {
	c, err := s.repo.TakeChallenge(ctx, id, purpose)
	if err != nil {
		return nil, err
	}
	if c == nil || !s.auth.now().Before(c.ExpiresAt) {
		return nil, errPasskeyChallenge()
	}
	return c, nil
}

// BeginRegistration returns the options for creating a new passkey.
func (s *PasskeyService) BeginRegistration(ctx context.Context, userID uuid.UUID) (uuid.UUID, *protocol.PublicKeyCredentialCreationOptions, error) {
	if s.web == nil {
		return uuid.Nil, nil, errPasskeysDisabled()
	}
	owner, err := s.customer(ctx, userID)
	if err != nil {
		return uuid.Nil, nil, err
	}
	if len(owner.passkeys) >= maxPasskeysPerUser {
		return uuid.Nil, nil, errPasskeyLimit()
	}
	creation, session, err := s.web.BeginRegistration(owner,
		webauthn.WithExclusions(webauthn.Credentials(owner.WebAuthnCredentials()).CredentialDescriptors()))
	if err != nil {
		return uuid.Nil, nil, apperror.Internal(err)
	}
	id, err := s.saveChallenge(ctx, &userID, purposeRegister, session)
	if err != nil {
		return uuid.Nil, nil, err
	}
	return id, &creation.Response, nil
}

// FinishRegistration verifies the authenticator's answer and stores the new
// passkey.
func (s *PasskeyService) FinishRegistration(ctx context.Context, userID, challengeID uuid.UUID, response []byte, platform string) (*Passkey, error) {
	if s.web == nil {
		return nil, errPasskeysDisabled()
	}
	challenge, err := s.takeChallenge(ctx, challengeID, purposeRegister)
	if err != nil {
		return nil, err
	}
	if challenge.UserID == nil || *challenge.UserID != userID {
		return nil, errPasskeyChallenge()
	}
	owner, err := s.customer(ctx, userID)
	if err != nil {
		return nil, err
	}
	parsed, err := protocol.ParseCredentialCreationResponseBytes(response)
	if err != nil {
		return nil, errPasskeyInvalid(err)
	}
	credential, err := s.web.CreateCredential(owner, challenge.Session, parsed)
	if err != nil {
		return nil, errPasskeyInvalid(err)
	}

	p := &Passkey{UserID: userID, CredentialID: credential.ID, Credential: *credential, Platform: platform}
	if err := s.repo.CreatePasskey(ctx, p, maxPasskeysPerUser); err != nil {
		return nil, err
	}
	return p, nil
}

// BeginLogin returns the options for signing in with any passkey the device
// holds for this service; no phone number is asked.
func (s *PasskeyService) BeginLogin(ctx context.Context) (uuid.UUID, *protocol.PublicKeyCredentialRequestOptions, error) {
	if s.web == nil {
		return uuid.Nil, nil, errPasskeysDisabled()
	}
	assertion, session, err := s.web.BeginDiscoverableLogin(webauthn.WithUserVerification(protocol.VerificationRequired))
	if err != nil {
		return uuid.Nil, nil, apperror.Internal(err)
	}
	id, err := s.saveChallenge(ctx, nil, purposeLogin, session)
	if err != nil {
		return uuid.Nil, nil, err
	}
	return id, &assertion.Response, nil
}

// FinishLogin verifies a passkey assertion and issues a token pair.
func (s *PasskeyService) FinishLogin(ctx context.Context, challengeID uuid.UUID, response []byte) (*TokenPair, error) {
	if s.web == nil {
		return nil, errPasskeysDisabled()
	}
	challenge, err := s.takeChallenge(ctx, challengeID, purposeLogin)
	if err != nil {
		return nil, err
	}
	parsed, err := protocol.ParseCredentialRequestResponseBytes(response)
	if err != nil {
		return nil, errPasskeyInvalid(err)
	}

	passkey, err := s.repo.FindPasskeyByCredentialID(ctx, parsed.RawID)
	if err != nil {
		return nil, err
	}
	if passkey == nil {
		return nil, apperror.BadRequest("passkey_unknown", "this passkey is not registered, sign in with an SMS code")
	}
	owner, err := s.customer(ctx, passkey.UserID)
	if err != nil {
		return nil, err
	}

	_, credential, err := s.web.ValidatePasskeyLogin(
		func(_, _ []byte) (webauthn.User, error) { return owner, nil },
		challenge.Session, parsed)
	if err != nil {
		return nil, errPasskeyInvalid(err)
	}
	if credential.Authenticator.CloneWarning {
		return nil, errPasskeyInvalid(errors.New("sign counter did not increase: possible cloned authenticator"))
	}

	now := s.auth.now()
	passkey.Credential = *credential
	passkey.LastUsedAt = &now
	if err := s.repo.SavePasskeyUse(ctx, passkey); err != nil {
		return nil, err
	}
	return s.auth.signIn(ctx, owner.user, now)
}

// List returns the customer's passkeys, oldest first.
func (s *PasskeyService) List(ctx context.Context, userID uuid.UUID) ([]Passkey, error) {
	if s.web == nil {
		return nil, errPasskeysDisabled()
	}
	return s.repo.ListPasskeys(ctx, userID)
}

// Delete removes one of the customer's passkeys. Sessions it already opened
// stay valid.
func (s *PasskeyService) Delete(ctx context.Context, userID, id uuid.UUID) error {
	if s.web == nil {
		return errPasskeysDisabled()
	}
	deleted, err := s.repo.DeletePasskey(ctx, userID, id)
	if err != nil {
		return err
	}
	if !deleted {
		return apperror.NotFound("passkey_not_found", "passkey not found")
	}
	return nil
}
