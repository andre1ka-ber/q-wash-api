// Package clientinfo records which kind of client made a request — the
// platform (android/ios/web/other) and app version — so logins can be
// counted per device for the admin's "Аналитика → Платформа" view.
//
// Apps announce themselves with the X-Client-Platform and X-App-Version
// headers. Without them the platform is guessed coarsely from the
// User-Agent ("web" for a browser, otherwise "other"). Only these buckets
// are kept: neither the raw User-Agent nor the IP is ever stored.
package clientinfo

import (
	"context"
	"net/http"
	"regexp"
	"strings"
)

const (
	HeaderPlatform = "X-Client-Platform"
	HeaderVersion  = "X-App-Version"

	PlatformAndroid = "android"
	PlatformIOS     = "ios"
	PlatformWeb     = "web"
	PlatformOther   = "other"
)

type Info struct {
	Platform string
	// Version is empty when the client sent none or an unusable one.
	Version string
}

type ctxKey struct{}

var versionRe = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z.+_-]{0,31}$`)

// FromRequest classifies a request. It never fails: unknown input maps to
// PlatformOther with no version.
func FromRequest(r *http.Request) Info {
	var info Info

	switch strings.ToLower(strings.TrimSpace(r.Header.Get(HeaderPlatform))) {
	case PlatformAndroid:
		info.Platform = PlatformAndroid
	case PlatformIOS:
		info.Platform = PlatformIOS
	case PlatformWeb:
		info.Platform = PlatformWeb
	default:
		if strings.Contains(r.UserAgent(), "Mozilla/") {
			info.Platform = PlatformWeb
		} else {
			info.Platform = PlatformOther
		}
	}

	if v := strings.TrimSpace(r.Header.Get(HeaderVersion)); versionRe.MatchString(v) {
		info.Version = v
	}
	return info
}

// Middleware stores the request's Info in its context.
func Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, FromRequest(r))))
	})
}

// From returns the Info Middleware stored, or PlatformOther without one
// (background jobs, tests that call services directly).
func From(ctx context.Context) Info {
	if info, ok := ctx.Value(ctxKey{}).(Info); ok {
		return info
	}
	return Info{Platform: PlatformOther}
}
