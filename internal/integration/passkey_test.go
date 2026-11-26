//go:build integration

package integration

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/go-webauthn/webauthn/protocol/webauthncbor"
	"github.com/golang-migrate/migrate/v4"

	"q-wash-api/internal/config"
	"q-wash-api/internal/user"
)

const (
	passkeyRPID        = "qwash.test"
	passkeyIOSAppID    = "TEAM123456.app.qwash.test"
	passkeyPackage     = "com.qwash.test"
	passkeyFingerprint = "AB:CD:EF:01:23:45:67:89:AB:CD:EF:01:23:45:67:89:AB:CD:EF:01:23:45:67:89:AB:CD:EF:01:23:45:67:89"
	iosOrigin          = "https://" + passkeyRPID

	flagUserPresent    = 0x01
	flagUserVerified   = 0x04
	flagBackupEligible = 0x08
	flagBackupState    = 0x10
	flagAttestedData   = 0x40
)

func withPasskeys(c *config.Config) {
	c.Passkey = config.PasskeyConfig{
		RPID:              passkeyRPID,
		IOSAppID:          passkeyIOSAppID,
		AndroidPackage:    passkeyPackage,
		AndroidCertSHA256: []string{passkeyFingerprint},
	}
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

// softAuthenticator plays the phone's side of a passkey ceremony with a real
// ES256 key.
type softAuthenticator struct {
	key          *ecdsa.PrivateKey
	credentialID []byte
	userHandle   string
	origin       string
	flags        byte
	counter      uint32
}

func newSoftAuthenticator(t *testing.T) *softAuthenticator {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	id := make([]byte, 20)
	if _, err := rand.Read(id); err != nil {
		t.Fatalf("generate credential id: %v", err)
	}
	return &softAuthenticator{
		key:          key,
		credentialID: id,
		origin:       iosOrigin,
		flags:        flagUserPresent | flagUserVerified | flagBackupEligible | flagBackupState,
	}
}

func (a *softAuthenticator) clientData(t *testing.T, ceremony, challenge string) []byte {
	t.Helper()
	raw, err := json.Marshal(map[string]string{"type": ceremony, "challenge": challenge, "origin": a.origin})
	if err != nil {
		t.Fatalf("marshal client data: %v", err)
	}
	return raw
}

func (a *softAuthenticator) authData(flags byte) []byte {
	rpHash := sha256.Sum256([]byte(passkeyRPID))
	data := append(rpHash[:], flags)
	return binary.BigEndian.AppendUint32(data, a.counter)
}

// create answers registration options with a new credential.
func (a *softAuthenticator) create(t *testing.T, options map[string]any) map[string]any {
	t.Helper()
	a.userHandle = options["user"].(map[string]any)["id"].(string)

	point, err := a.key.PublicKey.Bytes()
	if err != nil {
		t.Fatalf("encode public key: %v", err)
	}
	publicKey, err := webauthncbor.Marshal(map[int]any{1: 2, 3: -7, -1: 1, -2: point[1:33], -3: point[33:]})
	if err != nil {
		t.Fatalf("marshal public key: %v", err)
	}
	authData := a.authData(a.flags | flagAttestedData)
	authData = append(authData, make([]byte, 16)...) // AAGUID
	authData = binary.BigEndian.AppendUint16(authData, uint16(len(a.credentialID)))
	authData = append(append(authData, a.credentialID...), publicKey...)

	attestation, err := webauthncbor.Marshal(map[string]any{"fmt": "none", "attStmt": map[string]any{}, "authData": authData})
	if err != nil {
		t.Fatalf("marshal attestation: %v", err)
	}
	return map[string]any{
		"id":    b64(a.credentialID),
		"rawId": b64(a.credentialID),
		"type":  "public-key",
		"response": map[string]any{
			"clientDataJSON":    b64(a.clientData(t, "webauthn.create", options["challenge"].(string))),
			"attestationObject": b64(attestation),
			"transports":        []string{"internal"},
		},
	}
}

// get answers sign-in options with a signed assertion.
func (a *softAuthenticator) get(t *testing.T, options map[string]any) map[string]any {
	t.Helper()
	clientData := a.clientData(t, "webauthn.get", options["challenge"].(string))
	authData := a.authData(a.flags)
	clientHash := sha256.Sum256(clientData)
	digest := sha256.Sum256(append(append([]byte{}, authData...), clientHash[:]...))
	signature, err := ecdsa.SignASN1(rand.Reader, a.key, digest[:])
	if err != nil {
		t.Fatalf("sign assertion: %v", err)
	}
	return map[string]any{
		"id":    b64(a.credentialID),
		"rawId": b64(a.credentialID),
		"type":  "public-key",
		"response": map[string]any{
			"clientDataJSON":    b64(clientData),
			"authenticatorData": b64(authData),
			"signature":         b64(signature),
			"userHandle":        a.userHandle,
		},
	}
}

func (e *testEnv) passkeyOptions(t *testing.T, path, token string) (string, map[string]any) {
	t.Helper()
	resp := e.do(t, http.MethodPost, path, token, nil)
	if resp.status != http.StatusOK {
		t.Fatalf("%s: expected 200, got %d (%v)", path, resp.status, resp.body)
	}
	options, ok := resp.body["public_key"].(map[string]any)
	if !ok {
		t.Fatalf("%s: no public_key in %v", path, resp.body)
	}
	return resp.str("challenge_id"), options
}

func (e *testEnv) passkeyVerify(t *testing.T, path, token, challengeID string, credential map[string]any) apiResponse {
	t.Helper()
	return e.do(t, http.MethodPost, path, token, map[string]any{"challenge_id": challengeID, "credential": credential})
}

func (e *testEnv) registerPasskey(t *testing.T, token string, a *softAuthenticator) apiResponse {
	t.Helper()
	id, options := e.passkeyOptions(t, "/api/v1/auth/passkeys/register/options", token)
	return e.passkeyVerify(t, "/api/v1/auth/passkeys/register/verify", token, id, a.create(t, options))
}

func (e *testEnv) passkeyLogin(t *testing.T, a *softAuthenticator) apiResponse {
	t.Helper()
	id, options := e.passkeyOptions(t, "/api/v1/auth/passkeys/login/options", "")
	return e.passkeyVerify(t, "/api/v1/auth/passkeys/login/verify", "", id, a.get(t, options))
}

func (e *testEnv) listPasskeys(t *testing.T, token string) []map[string]any {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, e.baseURL+"/api/v1/auth/passkeys", nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := e.client.Do(req)
	if err != nil {
		t.Fatalf("list passkeys: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("list passkeys: %d %s %v", resp.StatusCode, body, err)
	}
	var items []map[string]any
	if err := json.Unmarshal(body, &items); err != nil {
		t.Fatalf("decode passkeys %s: %v", body, err)
	}
	return items
}

func expectError(t *testing.T, resp apiResponse, status int, code string) {
	t.Helper()
	got, _ := resp.body["error"].(map[string]any)
	if resp.status != status || got["code"] != code {
		t.Fatalf("expected %d %s, got %d (%v)", status, code, resp.status, resp.body)
	}
}

func TestPasskeys_RegisterSignInAndDelete(t *testing.T) {
	env := newTestEnv(t, withPasskeys)
	token := env.loginAs(t, "+992901000001", "")
	phone := newSoftAuthenticator(t)

	if got := env.listPasskeys(t, token); len(got) != 0 {
		t.Fatalf("expected no passkeys yet, got %v", got)
	}

	created := env.registerPasskey(t, token, phone)
	if created.status != http.StatusCreated || created.str("credential_id") != b64(phone.credentialID) {
		t.Fatalf("expected the new passkey, got %d (%v)", created.status, created.body)
	}
	if _, used := created.body["last_used_at"]; used {
		t.Fatalf("a new passkey has not been used: %v", created.body)
	}

	login := env.passkeyLogin(t, phone)
	if login.status != http.StatusOK {
		t.Fatalf("expected 200, got %d (%v)", login.status, login.body)
	}
	me := env.do(t, http.MethodGet, "/api/v1/me", login.str("access_token"), nil)
	if me.str("phone_number") != "+992901000001" || me.str("role") != "customer" {
		t.Fatalf("signed in as the wrong account: %v", me.body)
	}
	if listed := env.listPasskeys(t, token); len(listed) != 1 || listed[0]["last_used_at"] == nil {
		t.Fatalf("expected one used passkey, got %v", listed)
	}

	other := env.loginAs(t, "+992901000002", "")
	path := "/api/v1/auth/passkeys/" + created.str("id")
	expectError(t, env.do(t, http.MethodDelete, path, other, nil), http.StatusNotFound, "passkey_not_found")
	if len(env.listPasskeys(t, other)) != 0 {
		t.Fatal("another customer must not see the passkey")
	}
	if resp := env.do(t, http.MethodDelete, path, token, nil); resp.status != http.StatusNoContent {
		t.Fatalf("expected 204, got %d (%v)", resp.status, resp.body)
	}
	expectError(t, env.passkeyLogin(t, phone), http.StatusBadRequest, "passkey_unknown")
}

func TestPasskeys_AndroidOrigin(t *testing.T) {
	env := newTestEnv(t, withPasskeys)
	token := env.loginAs(t, "+992901000003", "")
	origins, err := env.cfg.Passkey.AndroidOrigins()
	if err != nil {
		t.Fatal(err)
	}
	phone := newSoftAuthenticator(t)
	phone.origin = origins[0]

	if resp := env.registerPasskey(t, token, phone); resp.status != http.StatusCreated {
		t.Fatalf("expected 201, got %d (%v)", resp.status, resp.body)
	}
	if resp := env.passkeyLogin(t, phone); resp.status != http.StatusOK {
		t.Fatalf("expected 200, got %d (%v)", resp.status, resp.body)
	}
}

func TestPasskeys_RefusedCeremonies(t *testing.T) {
	env := newTestEnv(t, withPasskeys)
	token := env.loginAs(t, "+992901000004", "")
	phone := newSoftAuthenticator(t)
	if resp := env.registerPasskey(t, token, phone); resp.status != http.StatusCreated {
		t.Fatalf("expected 201, got %d (%v)", resp.status, resp.body)
	}
	const verify = "/api/v1/auth/passkeys/login/verify"

	t.Run("a challenge works once", func(t *testing.T) {
		id, options := env.passkeyOptions(t, "/api/v1/auth/passkeys/login/options", "")
		assertion := phone.get(t, options)
		if resp := env.passkeyVerify(t, verify, "", id, assertion); resp.status != http.StatusOK {
			t.Fatalf("expected 200, got %d (%v)", resp.status, resp.body)
		}
		expectError(t, env.passkeyVerify(t, verify, "", id, assertion), http.StatusBadRequest, "passkey_challenge_invalid")
	})

	t.Run("an expired challenge is refused", func(t *testing.T) {
		id, options := env.passkeyOptions(t, "/api/v1/auth/passkeys/login/options", "")
		if err := env.db.Exec("UPDATE passkey_challenges SET expires_at = ? WHERE id = ?", time.Now().Add(-time.Second), id).Error; err != nil {
			t.Fatal(err)
		}
		expectError(t, env.passkeyVerify(t, verify, "", id, phone.get(t, options)), http.StatusBadRequest, "passkey_challenge_invalid")
	})

	t.Run("a registration challenge cannot sign in", func(t *testing.T) {
		id, options := env.passkeyOptions(t, "/api/v1/auth/passkeys/register/options", token)
		expectError(t, env.passkeyVerify(t, verify, "", id, phone.get(t, options)), http.StatusBadRequest, "passkey_challenge_invalid")
	})

	t.Run("another origin is refused", func(t *testing.T) {
		phone.origin = "https://evil.test"
		defer func() { phone.origin = iosOrigin }()
		expectError(t, env.passkeyLogin(t, phone), http.StatusBadRequest, "passkey_invalid")
	})

	t.Run("user verification is required", func(t *testing.T) {
		all := phone.flags
		phone.flags &^= flagUserVerified
		defer func() { phone.flags = all }()
		expectError(t, env.passkeyLogin(t, phone), http.StatusBadRequest, "passkey_invalid")
	})

	t.Run("a signature from another key is refused", func(t *testing.T) {
		forger := newSoftAuthenticator(t)
		forger.credentialID, forger.userHandle = phone.credentialID, phone.userHandle
		expectError(t, env.passkeyLogin(t, forger), http.StatusBadRequest, "passkey_invalid")
	})

	t.Run("a counter going backwards is refused", func(t *testing.T) {
		phone.counter = 5
		if resp := env.passkeyLogin(t, phone); resp.status != http.StatusOK {
			t.Fatalf("expected 200, got %d (%v)", resp.status, resp.body)
		}
		phone.counter = 3
		expectError(t, env.passkeyLogin(t, phone), http.StatusBadRequest, "passkey_invalid")
	})

	t.Run("the same credential cannot be registered twice", func(t *testing.T) {
		other := env.loginAs(t, "+992901000005", "")
		expectError(t, env.registerPasskey(t, other, phone), http.StatusConflict, "passkey_exists")
	})

	t.Run("a challenge belongs to the account that asked for it", func(t *testing.T) {
		other := env.loginAs(t, "+992901000006", "")
		id, options := env.passkeyOptions(t, "/api/v1/auth/passkeys/register/options", token)
		expectError(t, env.passkeyVerify(t, "/api/v1/auth/passkeys/register/verify", other, id, newSoftAuthenticator(t).create(t, options)),
			http.StatusBadRequest, "passkey_challenge_invalid")
	})
}

func TestPasskeys_LimitPerAccount(t *testing.T) {
	env := newTestEnv(t, withPasskeys)
	token := env.loginAs(t, "+992901000007", "")
	for i := range 10 {
		if resp := env.registerPasskey(t, token, newSoftAuthenticator(t)); resp.status != http.StatusCreated {
			t.Fatalf("passkey %d: expected 201, got %d (%v)", i+1, resp.status, resp.body)
		}
	}
	expectError(t, env.do(t, http.MethodPost, "/api/v1/auth/passkeys/register/options", token, nil), http.StatusConflict, "passkey_limit")
}

func TestPasskeys_CustomersOnly(t *testing.T) {
	env := newTestEnv(t, withPasskeys)
	admin := env.loginAs(t, "+992901000008", user.RoleAdmin)

	expectError(t, env.do(t, http.MethodPost, "/api/v1/auth/passkeys/register/options", admin, nil), http.StatusForbidden, "forbidden")
	expectError(t, env.do(t, http.MethodGet, "/api/v1/auth/passkeys", admin, nil), http.StatusForbidden, "forbidden")
	expectError(t, env.do(t, http.MethodGet, "/api/v1/auth/passkeys", "", nil), http.StatusUnauthorized, "unauthenticated")
}

// A customer who is later given another role keeps the row but must not get in
// with it.
func TestPasskeys_NoSignInAfterRoleChange(t *testing.T) {
	env := newTestEnv(t, withPasskeys)
	token := env.loginAs(t, "+992901000009", "")
	phone := newSoftAuthenticator(t)
	if resp := env.registerPasskey(t, token, phone); resp.status != http.StatusCreated {
		t.Fatalf("expected 201, got %d (%v)", resp.status, resp.body)
	}
	env.promoteToRole(t, "+992901000009", user.RoleStaff)
	expectError(t, env.passkeyLogin(t, phone), http.StatusForbidden, "forbidden")
}

func TestPasskeys_AssociationFiles(t *testing.T) {
	env := newTestEnv(t, withPasskeys)

	apple := env.do(t, http.MethodGet, "/.well-known/apple-app-site-association", "", nil)
	apps, _ := apple.body["webcredentials"].(map[string]any)["apps"].([]any)
	if apple.status != http.StatusOK || len(apps) != 1 || apps[0] != passkeyIOSAppID {
		t.Fatalf("unexpected apple-app-site-association: %d (%v)", apple.status, apple.body)
	}

	resp, err := env.client.Get(env.baseURL + "/.well-known/assetlinks.json")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var links []struct {
		Relation []string `json:"relation"`
		Target   struct {
			Package      string   `json:"package_name"`
			Fingerprints []string `json:"sha256_cert_fingerprints"`
		} `json:"target"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&links); err != nil {
		t.Fatal(err)
	}
	if resp.Header.Get("Content-Type") != "application/json" || len(links) != 1 ||
		links[0].Target.Package != passkeyPackage || links[0].Target.Fingerprints[0] != passkeyFingerprint {
		t.Fatalf("unexpected assetlinks.json: %+v", links)
	}
}

func TestPasskeys_OffByDefault(t *testing.T) {
	env := newTestEnv(t)
	token := env.loginAs(t, "+992901000010", "")

	expectError(t, env.do(t, http.MethodPost, "/api/v1/auth/passkeys/login/options", "", nil), http.StatusNotFound, "passkeys_disabled")
	expectError(t, env.do(t, http.MethodGet, "/api/v1/auth/passkeys", token, nil), http.StatusNotFound, "passkeys_disabled")
	expectError(t, env.do(t, http.MethodGet, "/.well-known/assetlinks.json", "", nil), http.StatusNotFound, "passkeys_disabled")
}

func TestPasskeys_MigrationIsReversible(t *testing.T) {
	env := newTestEnv(t)
	m, err := migrate.New("file://../../migrations", env.dsn)
	if err != nil {
		t.Fatalf("create migrator: %v", err)
	}
	defer m.Close()

	if err := m.Migrate(35); err != nil {
		t.Fatalf("migrate down to 35: %v", err)
	}
	if env.db.Migrator().HasTable("passkeys") || env.db.Migrator().HasTable("passkey_challenges") {
		t.Fatal("the down migration must drop both tables")
	}
	if err := m.Up(); err != nil {
		t.Fatalf("migrate up again: %v", err)
	}
	if !env.db.Migrator().HasTable("passkeys") || !env.db.Migrator().HasTable("passkey_challenges") {
		t.Fatal("the up migration must create both tables")
	}
}
