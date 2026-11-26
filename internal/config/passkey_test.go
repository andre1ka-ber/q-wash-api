package config

import (
	"strings"
	"testing"
)

func validPasskey() PasskeyConfig {
	return PasskeyConfig{
		RPID:              "example.com",
		IOSAppID:          "TEAM.app.example",
		AndroidPackage:    "com.example",
		AndroidCertSHA256: []string{strings.Repeat("00:", 31) + "FF"},
	}
}

func TestPasskeyConfig(t *testing.T) {
	t.Run("empty turns passkeys off", func(t *testing.T) {
		if err := (PasskeyConfig{}).validate(); err != nil || (PasskeyConfig{}).Enabled() {
			t.Fatalf("expected a valid, disabled config, got %v", err)
		}
	})

	t.Run("complete is accepted", func(t *testing.T) {
		if err := validPasskey().validate(); err != nil || !validPasskey().Enabled() {
			t.Fatalf("expected a valid, enabled config, got %v", err)
		}
	})

	t.Run("Android origin is the base64url certificate hash", func(t *testing.T) {
		origins, err := validPasskey().AndroidOrigins()
		want := "android:apk-key-hash:" + strings.Repeat("A", 41) + "P8"
		if err != nil || len(origins) != 1 || origins[0] != want {
			t.Fatalf("expected %q, got %v (%v)", want, origins, err)
		}
	})

	for name, change := range map[string]func(*PasskeyConfig){
		"missing RP ID":           func(p *PasskeyConfig) { p.RPID = "" },
		"missing iOS app id":      func(p *PasskeyConfig) { p.IOSAppID = "" },
		"missing Android package": func(p *PasskeyConfig) { p.AndroidPackage = "" },
		"missing fingerprints":    func(p *PasskeyConfig) { p.AndroidCertSHA256 = nil },
		"RP ID with a scheme":     func(p *PasskeyConfig) { p.RPID = "https://example.com" },
		"short fingerprint":       func(p *PasskeyConfig) { p.AndroidCertSHA256 = []string{"AB:CD"} },
		"fingerprint not hex":     func(p *PasskeyConfig) { p.AndroidCertSHA256 = []string{"zz"} },
	} {
		t.Run(name, func(t *testing.T) {
			p := validPasskey()
			change(&p)
			c := Config{Passkey: p}
			if err := c.Validate(); err == nil {
				t.Fatal("expected a startup error")
			}
		})
	}
}
