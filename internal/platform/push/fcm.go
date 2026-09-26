package push

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"golang.org/x/oauth2/google"
)

const fcmScope = "https://www.googleapis.com/auth/firebase.messaging"

// FCM sends through the Firebase Cloud Messaging HTTP v1 API. It uses a
// service-account credential for OAuth and plain net/http instead of the
// full Firebase Admin SDK — one endpoint is all this needs.
type FCM struct {
	endpoint string
	client   *http.Client
}

// NewFCM builds a sender from a service-account JSON key. projectID is the
// Firebase project the tokens belong to.
func NewFCM(ctx context.Context, projectID string, serviceAccountJSON []byte) (*FCM, error) {
	creds, err := google.CredentialsFromJSON(ctx, serviceAccountJSON, fcmScope)
	if err != nil {
		return nil, fmt.Errorf("parse FCM service account: %w", err)
	}
	return newFCM(fmt.Sprintf("https://fcm.googleapis.com/v1/projects/%s/messages:send", projectID),
		&http.Client{Timeout: 10 * time.Second, Transport: &oauthTransport{creds: creds}}), nil
}

func newFCM(endpoint string, client *http.Client) *FCM {
	return &FCM{endpoint: endpoint, client: client}
}

type oauthTransport struct {
	creds *google.Credentials
}

func (t *oauthTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	tok, err := t.creds.TokenSource.Token()
	if err != nil {
		return nil, err
	}
	req = req.Clone(req.Context())
	tok.SetAuthHeader(req)
	return http.DefaultTransport.RoundTrip(req)
}

type fcmRequest struct {
	Message fcmMessage `json:"message"`
}

type fcmMessage struct {
	Token        string            `json:"token"`
	Notification fcmNotification   `json:"notification"`
	Data         map[string]string `json:"data,omitempty"`
}

type fcmNotification struct {
	Title string `json:"title"`
	Body  string `json:"body"`
}

func (f *FCM) Send(ctx context.Context, deviceToken string, msg Message) error {
	body, err := json.Marshal(fcmRequest{Message: fcmMessage{
		Token:        deviceToken,
		Notification: fcmNotification{Title: msg.Title, Body: msg.Body},
		Data:         msg.Data,
	}})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, f.endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := f.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		return nil
	}

	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	// FCM reports a dead token as 404 UNREGISTERED (or 400 INVALID_ARGUMENT
	// on a malformed one) with the code in error.details / error.status.
	var parsed struct {
		Error struct {
			Status  string `json:"status"`
			Details []struct {
				ErrorCode string `json:"errorCode"`
			} `json:"details"`
		} `json:"error"`
	}
	_ = json.Unmarshal(raw, &parsed)
	if resp.StatusCode == http.StatusNotFound || parsed.Error.Status == "NOT_FOUND" {
		return ErrUnregistered
	}
	for _, d := range parsed.Error.Details {
		if d.ErrorCode == "UNREGISTERED" {
			return ErrUnregistered
		}
	}
	return fmt.Errorf("fcm send: status %d: %s", resp.StatusCode, raw)
}
