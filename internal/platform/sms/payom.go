package sms

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

var (
	// Payom accepts only Tajik numbers in this exact shape.
	payomPhoneRegexp  = regexp.MustCompile(`^\+992\d{9}$`)
	payomSenderRegexp = regexp.MustCompile(`^[A-Za-z0-9.]{1,11}$`)
)

// PayomSender delivers OTP codes through Payom.tj's REST API
// (POST /api/message). It does not retry: the API documents no idempotency
// key, so a retry after a timeout could deliver the code twice, and the OTP
// request cooldown already limits user-driven resends.
type PayomSender struct {
	cfg    PayomConfig
	url    string
	client *http.Client
}

func NewPayomSender(cfg PayomConfig) (*PayomSender, error) {
	base := strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	if base == "" || cfg.Token == "" {
		return nil, errors.New("sms: payom requires PAYOM_BASE_URL and PAYOM_API_TOKEN")
	}
	if !payomSenderRegexp.MatchString(cfg.SenderName) {
		return nil, errors.New("sms: PAYOM_SENDER_NAME must be 1-11 Latin letters, digits or dots")
	}
	if !strings.Contains(base, "://") {
		base = "https://" + base
	}
	return &PayomSender{
		cfg:    cfg,
		url:    base + "/api/message",
		client: &http.Client{Timeout: 10 * time.Second},
	}, nil
}

type payomTemplate struct {
	TemplateID string         `json:"templateId"`
	Variables  map[string]any `json:"variables"`
}

type payomRequest struct {
	Telephone       string         `json:"telephone"`
	SenderName      string         `json:"senderName"`
	Type            string         `json:"type"`
	Text            string         `json:"text,omitempty"`
	TemplateMessage *payomTemplate `json:"templateMessage,omitempty"`
}

func (p *PayomSender) SendOTP(ctx context.Context, phoneNumber, code string, ttl time.Duration) error {
	if !payomPhoneRegexp.MatchString(phoneNumber) {
		return errors.New("sms: payom only delivers to +992 numbers")
	}

	req := payomRequest{Telephone: phoneNumber, SenderName: p.cfg.SenderName, Type: "SMS"}
	if p.cfg.OTPTemplateID != "" {
		req.TemplateMessage = &payomTemplate{
			TemplateID: p.cfg.OTPTemplateID,
			Variables:  map[string]any{"code": code},
		}
	} else {
		req.Text = OTPMessage(code, ttl)
	}
	body, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("sms: encode payom request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("sms: build payom request: %w", err)
	}
	httpReq.Header.Set("Authorization", "Bearer "+p.cfg.Token)
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")

	resp, err := p.client.Do(httpReq)
	if err != nil {
		// The URL in a *url.Error is only the account host; the token travels in a header.
		return fmt.Errorf("sms: payom request failed: %w", err)
	}
	defer resp.Body.Close()

	// 201 = queued. That is all Payom confirms synchronously; delivery
	// status only arrives by webhook, which this integration doesn't use.
	if resp.StatusCode == http.StatusCreated {
		return nil
	}
	return fmt.Errorf("sms: payom rejected the message: %s", payomErrorDetail(resp))
}

// payomErrorDetail summarises an error response: 422/400 carry RFC 7807
// {title, detail}, 401 carries {code, message}. The request token is never
// echoed by Payom, and is not included here either.
func payomErrorDetail(resp *http.Response) string {
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
	var e struct {
		Detail  string `json:"detail"`
		Message string `json:"message"`
	}
	detail := ""
	if json.Unmarshal(raw, &e) == nil {
		detail = e.Detail
		if detail == "" {
			detail = e.Message
		}
	}
	if detail == "" {
		return fmt.Sprintf("HTTP %d", resp.StatusCode)
	}
	return fmt.Sprintf("HTTP %d: %s", resp.StatusCode, detail)
}
