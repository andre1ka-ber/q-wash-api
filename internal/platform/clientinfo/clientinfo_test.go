package clientinfo

import (
	"context"
	"net/http/httptest"
	"testing"
)

func TestFromRequest(t *testing.T) {
	cases := []struct {
		name    string
		headers map[string]string
		want    Info
	}{
		{"explicit android", map[string]string{HeaderPlatform: "Android", HeaderVersion: "2.4.1"}, Info{PlatformAndroid, "2.4.1"}},
		{"explicit ios", map[string]string{HeaderPlatform: "ios", HeaderVersion: "2.4+7"}, Info{PlatformIOS, "2.4+7"}},
		{"browser UA without header is web", map[string]string{"User-Agent": "Mozilla/5.0 (X11) Chrome/120"}, Info{PlatformWeb, ""}},
		{"dart UA without header is other", map[string]string{"User-Agent": "Dart/3.4 (dart:io)"}, Info{PlatformOther, ""}},
		{"unknown platform header falls back to UA", map[string]string{HeaderPlatform: "toaster", "User-Agent": "curl/8"}, Info{PlatformOther, ""}},
		{"version with junk is dropped", map[string]string{HeaderPlatform: "web", HeaderVersion: "1.0 <script>"}, Info{PlatformWeb, ""}},
		{"over-long version is dropped", map[string]string{HeaderPlatform: "web", HeaderVersion: "1234567890123456789012345678901234567890"}, Info{PlatformWeb, ""}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/", nil)
			for k, v := range c.headers {
				r.Header.Set(k, v)
			}
			if got := FromRequest(r); got != c.want {
				t.Errorf("got %+v, want %+v", got, c.want)
			}
		})
	}
}

func TestFromContextDefaultsToOther(t *testing.T) {
	if got := From(context.Background()); got.Platform != PlatformOther {
		t.Errorf("got %+v", got)
	}
}
