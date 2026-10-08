package sms

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const testPhone = "+992901112233"

func newTestPayom(t *testing.T, handler http.HandlerFunc, mutate func(*PayomConfig)) *PayomSender {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	cfg := PayomConfig{BaseURL: srv.URL, Token: "secret-token", SenderName: "QWash"}
	if mutate != nil {
		mutate(&cfg)
	}
	p, err := NewPayomSender(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestPayomSendOTP_FreeText(t *testing.T) {
	var got map[string]any
	var auth, contentType, method, path string
	p := newTestPayom(t, func(w http.ResponseWriter, r *http.Request) {
		auth, contentType, method, path = r.Header.Get("Authorization"), r.Header.Get("Content-Type"), r.Method, r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"x","deliveryStatus":"SERVICE_ACCEPTED"}`))
	}, nil)

	if err := p.SendOTP(context.Background(), testPhone, "123456", 5*time.Minute); err != nil {
		t.Fatal(err)
	}
	if method != http.MethodPost || path != "/api/message" {
		t.Errorf("request = %s %s", method, path)
	}
	if auth != "Bearer secret-token" || contentType != "application/json" {
		t.Errorf("headers: auth=%q content-type=%q", auth, contentType)
	}
	if got["telephone"] != testPhone || got["senderName"] != "QWash" || got["type"] != "SMS" {
		t.Errorf("body = %v", got)
	}
	if got["text"] != OTPMessage("123456", 5*time.Minute) {
		t.Errorf("text = %v", got["text"])
	}
	if _, ok := got["templateMessage"]; ok {
		t.Errorf("free-text mode must not send templateMessage: %v", got)
	}
}

func TestPayomSendOTP_Template(t *testing.T) {
	var got struct {
		Text            string `json:"text"`
		TemplateMessage struct {
			TemplateID string         `json:"templateId"`
			Variables  map[string]any `json:"variables"`
		} `json:"templateMessage"`
	}
	p := newTestPayom(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(http.StatusCreated)
	}, func(c *PayomConfig) { c.OTPTemplateID = "11111111-2222-3333-4444-555555555555" })

	if err := p.SendOTP(context.Background(), testPhone, "654321", 5*time.Minute); err != nil {
		t.Fatal(err)
	}
	if got.TemplateMessage.TemplateID != "11111111-2222-3333-4444-555555555555" || got.TemplateMessage.Variables["code"] != "654321" {
		t.Errorf("templateMessage = %+v", got.TemplateMessage)
	}
	if got.Text != "" {
		t.Errorf("template mode must not send free text, got %q", got.Text)
	}
}

func TestPayomSendOTP_Errors(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   string
	}{
		{"unauthorized", 401, `{"code":401,"message":"JWT Token not found"}`, "HTTP 401: JWT Token not found"},
		{"validation", 422, `{"title":"An error occurred","detail":"telephone: bad"}`, "HTTP 422: telephone: bad"},
		{"server error with html body", 502, `<html>bad gateway</html>`, "HTTP 502"},
		{"unexpected 200", 200, `{}`, "HTTP 200"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := newTestPayom(t, func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}, nil)
			err := p.SendOTP(context.Background(), testPhone, "123456", time.Minute)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want it to contain %q", err, tc.want)
			}
			if strings.Contains(err.Error(), "secret-token") {
				t.Errorf("error leaks the token: %v", err)
			}
		})
	}
}

func TestPayomSendOTP_RejectsNonTajikNumbersWithoutCalling(t *testing.T) {
	called := false
	p := newTestPayom(t, func(http.ResponseWriter, *http.Request) { called = true }, nil)
	for _, phone := range []string{"+15551234567", "+99290111223", "+9929011122334", "992901112233"} {
		if err := p.SendOTP(context.Background(), phone, "123456", time.Minute); err == nil {
			t.Errorf("%q: expected an error", phone)
		}
	}
	if called {
		t.Error("an invalid number must not reach Payom")
	}
}

func TestPayomSendOTP_NetworkFailure(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()
	p, err := NewPayomSender(PayomConfig{BaseURL: url, Token: "t", SenderName: "QWash"})
	if err != nil {
		t.Fatal(err)
	}
	if err := p.SendOTP(context.Background(), testPhone, "123456", time.Minute); err == nil {
		t.Fatal("expected a connection error")
	}
}

func TestNewPayomSender_Validation(t *testing.T) {
	bad := []PayomConfig{
		{Token: "t", SenderName: "QWash"},
		{BaseURL: "h.example", SenderName: "QWash"},
		{BaseURL: "h.example", Token: "t", SenderName: ""},
		{BaseURL: "h.example", Token: "t", SenderName: "TwelveChars!!"},
		{BaseURL: "h.example", Token: "t", SenderName: "has space"},
	}
	for i, cfg := range bad {
		if _, err := NewPayomSender(cfg); err == nil {
			t.Errorf("case %d: expected a validation error", i)
		}
	}
	p, err := NewPayomSender(PayomConfig{BaseURL: " payom.example.tj/ ", Token: "t", SenderName: "QWash"})
	if err != nil {
		t.Fatal(err)
	}
	if p.url != "https://payom.example.tj/api/message" {
		t.Errorf("url = %q", p.url)
	}
}

func TestNewOTPSender(t *testing.T) {
	for _, provider := range []string{"", "stub"} {
		s, err := NewOTPSender(provider, PayomConfig{})
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := s.(*StubSender); !ok {
			t.Errorf("provider %q: got %T, want the stub", provider, s)
		}
	}
	s, err := NewOTPSender("payom", PayomConfig{BaseURL: "h.example", Token: "t", SenderName: "QWash"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := s.(*PayomSender); !ok {
		t.Errorf("got %T, want PayomSender", s)
	}
	if _, err := NewOTPSender("payom", PayomConfig{}); err == nil {
		t.Error("payom without credentials must fail at startup")
	}
	if _, err := NewOTPSender("twilio", PayomConfig{}); err == nil {
		t.Error("unknown provider must fail")
	}
}

func TestOTPMessage(t *testing.T) {
	msg := OTPMessage("123456", 5*time.Minute)
	if msg != "QWash: код подтверждения 123456. Действует 5 мин." {
		t.Errorf("msg = %q", msg)
	}
	if n := len([]rune(msg)); n > 70 {
		t.Errorf("%d characters won't fit one UCS-2 SMS segment", n)
	}
}
