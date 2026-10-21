package outbox

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"
)

type WebhookPublisher struct {
	url    string
	secret []byte
	client *http.Client
	now    func() time.Time
}

func NewWebhookPublisher(url, secret string) *WebhookPublisher {
	return &WebhookPublisher{
		url:    url,
		secret: []byte(secret),
		client: &http.Client{Timeout: 5 * time.Second},
		now:    time.Now,
	}
}

func (p *WebhookPublisher) Publish(ctx context.Context, m Message) error {
	timestamp := strconv.FormatInt(p.now().Unix(), 10)
	mac := hmac.New(sha256.New, p.secret)
	mac.Write([]byte(timestamp))
	mac.Write([]byte("."))
	mac.Write(m.Payload)
	signature := "t=" + timestamp + ",v1=" + hex.EncodeToString(mac.Sum(nil))

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.url, bytes.NewReader(m.Payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Inventory-Signature", signature)
	req.Header.Set("X-Outbox-ID", strconv.FormatInt(m.ID, 10))
	req.Header.Set("X-Outbox-Topic", m.Topic)
	req.Header.Set("X-Outbox-Attempt", strconv.Itoa(m.Attempts))
	req.Header.Set("X-Tenant-ID", m.TenantID)

	resp, err := p.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if _, err := io.Copy(io.Discard, io.LimitReader(resp.Body, 4096)); err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("webhook returned %s", resp.Status)
	}
	return nil
}

func signWebhook(secret, timestamp string, payload []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(timestamp))
	mac.Write([]byte("."))
	mac.Write(payload)
	return hex.EncodeToString(mac.Sum(nil))
}
