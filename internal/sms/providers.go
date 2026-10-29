package sms

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

var httpClient = &http.Client{Timeout: 15 * time.Second}

// Twilio sends via the Twilio Programmable Messaging REST API.
type Twilio struct {
	AccountSID string
	AuthToken  string
	From       string // default sender (number or alphanumeric ID)
	BaseURL    string // override for tests
}

func (t *Twilio) Name() string { return "twilio" }

func (t *Twilio) Send(ctx context.Context, m Message) (string, error) {
	base := t.BaseURL
	if base == "" {
		base = "https://api.twilio.com"
	}
	from := m.SenderID
	if from == "" {
		from = t.From
	}
	form := url.Values{"To": {m.To}, "From": {from}, "Body": {m.Body}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		fmt.Sprintf("%s/2010-04-01/Accounts/%s/Messages.json", base, t.AccountSID), strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.SetBasicAuth(t.AccountSID, t.AuthToken)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var body struct {
		SID     string `json:"sid"`
		Message string `json:"message"`
		Code    int    `json:"code"`
	}
	_ = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&body)
	switch {
	case resp.StatusCode < 300:
		return body.SID, nil
	case resp.StatusCode == http.StatusBadRequest:
		return "", &PermanentError{fmt.Errorf("twilio %d: %s", body.Code, body.Message)}
	default:
		return "", fmt.Errorf("twilio http %d: %s", resp.StatusCode, body.Message)
	}
}

// Zenziva is a popular Indonesian SMS gateway (reguler API).
type Zenziva struct {
	UserKey string
	PassKey string
	BaseURL string
}

func (z *Zenziva) Name() string { return "zenziva" }

func (z *Zenziva) Send(ctx context.Context, m Message) (string, error) {
	base := z.BaseURL
	if base == "" {
		base = "https://console.zenziva.net"
	}
	// Zenziva expects local format without '+'.
	form := url.Values{"userkey": {z.UserKey}, "passkey": {z.PassKey}, "to": {strings.TrimPrefix(m.To, "+")}, "message": {m.Body}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/reguler/api/sendsms/", strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var body struct {
		MessageID string `json:"messageId"`
		Status    string `json:"status"`
		Text      string `json:"text"`
	}
	_ = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&body)
	if resp.StatusCode >= 500 {
		return "", fmt.Errorf("zenziva http %d", resp.StatusCode)
	}
	if body.Status != "1" {
		return "", &PermanentError{fmt.Errorf("zenziva: %s", body.Text)}
	}
	return body.MessageID, nil
}
