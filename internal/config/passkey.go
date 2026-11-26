package config

import (
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

// PasskeyConfig enables customer passkey sign-in. All fields empty means the
// feature is off.
type PasskeyConfig struct {
	// RPID is the domain passkeys are bound to. It must serve the two
	// /.well-known/ association files; changing it invalidates every passkey.
	RPID string
	// IOSAppID is "<team id>.<bundle id>".
	IOSAppID       string
	AndroidPackage string
	// AndroidCertSHA256 are the signing certificate fingerprints as Play
	// Console and keytool print them (colon-separated hex).
	AndroidCertSHA256 []string
}

func (p PasskeyConfig) Enabled() bool { return p.RPID != "" }

// IOSOrigin is the origin iOS reports in a passkey ceremony.
func (p PasskeyConfig) IOSOrigin() string { return "https://" + p.RPID }

// AndroidOrigins are the origins Android reports, one per signing certificate.
func (p PasskeyConfig) AndroidOrigins() ([]string, error) {
	origins := make([]string, 0, len(p.AndroidCertSHA256))
	for _, fingerprint := range p.AndroidCertSHA256 {
		raw, err := hex.DecodeString(strings.ReplaceAll(fingerprint, ":", ""))
		if err != nil || len(raw) != 32 {
			return nil, fmt.Errorf("PASSKEY_ANDROID_CERT_SHA256: %q is not a SHA-256 fingerprint", fingerprint)
		}
		origins = append(origins, "android:apk-key-hash:"+base64.RawURLEncoding.EncodeToString(raw))
	}
	return origins, nil
}

func (p PasskeyConfig) validate() error {
	set := []bool{p.RPID != "", p.IOSAppID != "", p.AndroidPackage != "", len(p.AndroidCertSHA256) > 0}
	anySet, allSet := false, true
	for _, s := range set {
		anySet = anySet || s
		allSet = allSet && s
	}
	if !anySet {
		return nil
	}
	if !allSet {
		return errors.New("PASSKEY_RP_ID, PASSKEY_IOS_APP_ID, PASSKEY_ANDROID_PACKAGE and PASSKEY_ANDROID_CERT_SHA256 must be set together")
	}
	if strings.ContainsAny(p.RPID, "/:") {
		return errors.New("PASSKEY_RP_ID must be a bare domain, e.g. example.com")
	}
	_, err := p.AndroidOrigins()
	return err
}
