package auth

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"q-wash-api/internal/apperror"
	"q-wash-api/internal/config"
	"q-wash-api/internal/httputil"
	"q-wash-api/internal/platform/audit"
	"q-wash-api/internal/platform/clientinfo"
)

type PasskeyHandler struct {
	service *PasskeyService
	cfg     config.PasskeyConfig
}

func NewPasskeyHandler(service *PasskeyService, cfg config.PasskeyConfig) *PasskeyHandler {
	return &PasskeyHandler{service: service, cfg: cfg}
}

// RegisterPublicRoutes mounts passkey sign-in. Each limiter may be nil.
func (h *PasskeyHandler) RegisterPublicRoutes(r chi.Router, options, verify func(http.Handler) http.Handler) {
	r.With(orPassthrough(options)).Post("/auth/passkeys/login/options", h.loginOptions)
	r.With(orPassthrough(verify)).Post("/auth/passkeys/login/verify", h.loginVerify)
}

// RegisterCustomerRoutes mounts passkey management. The caller must wrap r
// with authentication restricted to the customer role.
func (h *PasskeyHandler) RegisterCustomerRoutes(r chi.Router) {
	r.Post("/auth/passkeys/register/options", h.registerOptions)
	r.Post("/auth/passkeys/register/verify", h.registerVerify)
	r.Get("/auth/passkeys", h.list)
	r.Delete("/auth/passkeys/{id}", h.delete)
}

// RegisterWellKnownRoutes mounts, outside /api/v1, the two files iOS and
// Android fetch from the passkey domain to trust the app.
func (h *PasskeyHandler) RegisterWellKnownRoutes(r chi.Router) {
	r.Get("/.well-known/apple-app-site-association", h.appleAppSiteAssociation)
	r.Get("/.well-known/assetlinks.json", h.assetLinks)
}

type passkeyOptionsResponse struct {
	ChallengeID string `json:"challenge_id"`
	PublicKey   any    `json:"public_key"`
}

type passkeyVerifyRequest struct {
	ChallengeID string          `json:"challenge_id"`
	Credential  json.RawMessage `json:"credential"`
}

type passkeyResponse struct {
	ID           string     `json:"id"`
	CredentialID string     `json:"credential_id"`
	Platform     string     `json:"platform"`
	Provider     string     `json:"provider,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
	LastUsedAt   *time.Time `json:"last_used_at,omitempty"`
}

func toPasskeyResponse(p Passkey) passkeyResponse {
	return passkeyResponse{
		ID:           p.ID.String(),
		CredentialID: base64.RawURLEncoding.EncodeToString(p.CredentialID),
		Platform:     p.Platform,
		Provider:     passkeyProvider(p.Credential.Authenticator.AAGUID),
		CreatedAt:    p.CreatedAt,
		LastUsedAt:   p.LastUsedAt,
	}
}

func decodePasskeyVerify(r *http.Request) (uuid.UUID, []byte, error) {
	var req passkeyVerifyRequest
	if err := httputil.DecodeJSON(r, &req); err != nil {
		return uuid.Nil, nil, err
	}
	id, err := uuid.Parse(req.ChallengeID)
	if err != nil || len(req.Credential) == 0 {
		return uuid.Nil, nil, apperror.BadRequest("invalid_body", "challenge_id and credential are required")
	}
	return id, req.Credential, nil
}

// passkeyReason is the library's explanation of a refused ceremony, for the
// audit log.
func passkeyReason(err error) string {
	var appErr *apperror.Error
	if errors.As(err, &appErr) && appErr.Err != nil {
		return audit.Truncate(appErr.Err.Error(), 200)
	}
	return ""
}

func (h *PasskeyHandler) loginOptions(w http.ResponseWriter, r *http.Request) {
	id, options, err := h.service.BeginLogin(r.Context())
	if err != nil {
		httputil.WriteError(w, r, err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, passkeyOptionsResponse{ChallengeID: id.String(), PublicKey: options})
}

func (h *PasskeyHandler) loginVerify(w http.ResponseWriter, r *http.Request) {
	challengeID, credential, err := decodePasskeyVerify(r)
	if err != nil {
		httputil.WriteError(w, r, err)
		return
	}
	pair, err := h.service.FinishLogin(r.Context(), challengeID, credential)
	if err != nil {
		audit.Warn(r, "auth.passkey_login_failed", "code", audit.ErrorCode(err), "reason", passkeyReason(err))
		httputil.WriteError(w, r, err)
		return
	}
	audit.Info(r, "auth.passkey_login", "user_id", pair.User.ID.String())
	httputil.WriteJSON(w, http.StatusOK, toTokenPairResponse(pair))
}

func (h *PasskeyHandler) registerOptions(w http.ResponseWriter, r *http.Request) {
	authUser, ok := httputil.AuthUser(w, r)
	if !ok {
		return
	}
	id, options, err := h.service.BeginRegistration(r.Context(), authUser.ID)
	if err != nil {
		httputil.WriteError(w, r, err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, passkeyOptionsResponse{ChallengeID: id.String(), PublicKey: options})
}

func (h *PasskeyHandler) registerVerify(w http.ResponseWriter, r *http.Request) {
	authUser, ok := httputil.AuthUser(w, r)
	if !ok {
		return
	}
	challengeID, credential, err := decodePasskeyVerify(r)
	if err != nil {
		httputil.WriteError(w, r, err)
		return
	}
	p, err := h.service.FinishRegistration(r.Context(), authUser.ID, challengeID, credential, clientinfo.From(r.Context()).Platform)
	if err != nil {
		audit.Warn(r, "auth.passkey_register_failed", "user_id", authUser.ID.String(), "code", audit.ErrorCode(err), "reason", passkeyReason(err))
		httputil.WriteError(w, r, err)
		return
	}
	audit.Info(r, "auth.passkey_registered", "user_id", authUser.ID.String(), "passkey_id", p.ID.String())
	httputil.WriteJSON(w, http.StatusCreated, toPasskeyResponse(*p))
}

func (h *PasskeyHandler) list(w http.ResponseWriter, r *http.Request) {
	authUser, ok := httputil.AuthUser(w, r)
	if !ok {
		return
	}
	passkeys, err := h.service.List(r.Context(), authUser.ID)
	if err != nil {
		httputil.WriteError(w, r, err)
		return
	}
	items := make([]passkeyResponse, len(passkeys))
	for i, p := range passkeys {
		items[i] = toPasskeyResponse(p)
	}
	httputil.WriteJSON(w, http.StatusOK, items)
}

func (h *PasskeyHandler) delete(w http.ResponseWriter, r *http.Request) {
	authUser, ok := httputil.AuthUser(w, r)
	if !ok {
		return
	}
	id, err := httputil.ParseUUIDParam(r, "id")
	if err != nil {
		httputil.WriteError(w, r, err)
		return
	}
	if err := h.service.Delete(r.Context(), authUser.ID, id); err != nil {
		httputil.WriteError(w, r, err)
		return
	}
	audit.Info(r, "auth.passkey_deleted", "user_id", authUser.ID.String(), "passkey_id", id.String())
	w.WriteHeader(http.StatusNoContent)
}

func (h *PasskeyHandler) appleAppSiteAssociation(w http.ResponseWriter, r *http.Request) {
	if !h.cfg.Enabled() {
		httputil.WriteError(w, r, errPasskeysDisabled())
		return
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"webcredentials": map[string]any{"apps": []string{h.cfg.IOSAppID}},
	})
}

func (h *PasskeyHandler) assetLinks(w http.ResponseWriter, r *http.Request) {
	if !h.cfg.Enabled() {
		httputil.WriteError(w, r, errPasskeysDisabled())
		return
	}
	httputil.WriteJSON(w, http.StatusOK, []map[string]any{{
		"relation": []string{
			"delegate_permission/common.handle_all_urls",
			"delegate_permission/common.get_login_creds",
		},
		"target": map[string]any{
			"namespace":                "android_app",
			"package_name":             h.cfg.AndroidPackage,
			"sha256_cert_fingerprints": h.cfg.AndroidCertSHA256,
		},
	}})
}
