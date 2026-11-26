package auth

import (
	"testing"

	"github.com/google/uuid"
)

func TestPasskeyProvider(t *testing.T) {
	known := uuid.MustParse("fbfc3007-154e-4ecc-8c0b-6e020557d7bd")
	for name, c := range map[string]struct {
		aaguid []byte
		want   string
	}{
		"known":        {known[:], "iCloud Keychain"},
		"zero":         {make([]byte, 16), ""},
		"missing":      {nil, ""},
		"wrong length": {[]byte{1, 2}, ""},
	} {
		if got := passkeyProvider(c.aaguid); got != c.want {
			t.Errorf("%s: expected %q, got %q", name, c.want, got)
		}
	}
}
