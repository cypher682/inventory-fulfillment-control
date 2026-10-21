package outbox

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestWebhookPublisherSignsAndDelivers(t *testing.T) {
	const secret = "0123456789abcdef"
	payload := []byte(`{"event_type":"inventory.received"}`)
	now := time.Unix(1700000000, 0)

	var gotBody []byte
	var gotSignature, gotID, gotTenant, gotTopic string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s", r.Method)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read body: %v", err)
		}
		gotBody = body
		gotSignature = r.Header.Get("X-Inventory-Signature")
		gotID = r.Header.Get("X-Outbox-ID")
		gotTenant = r.Header.Get("X-Tenant-ID")
		gotTopic = r.Header.Get("X-Outbox-Topic")
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	publisher := NewWebhookPublisher(server.URL, secret)
	publisher.client = server.Client()
	publisher.now = func() time.Time { return now }

	err := publisher.Publish(context.Background(), Message{
		ID:       42,
		TenantID: "demo",
		Topic:    "inventory.events",
		Payload:  payload,
		Attempts: 2,
	})
	if err != nil {
		t.Fatalf("publish: %v", err)
	}

	wantSignature := "t=1700000000,v1=" + signWebhook(secret, "1700000000", payload)
	if gotSignature != wantSignature {
		t.Fatalf("signature = %q, want %q", gotSignature, wantSignature)
	}
	if string(gotBody) != string(payload) {
		t.Fatalf("body = %s", gotBody)
	}
	if gotID != "42" || gotTenant != "demo" || gotTopic != "inventory.events" {
		t.Fatalf("headers = id=%q tenant=%q topic=%q", gotID, gotTenant, gotTopic)
	}
}

func TestWebhookPublisherRejectsFailureStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "nope", http.StatusBadGateway)
	}))
	defer server.Close()

	publisher := NewWebhookPublisher(server.URL, "0123456789abcdef")
	publisher.client = server.Client()

	err := publisher.Publish(context.Background(), Message{ID: 1, Topic: "inventory.events", Payload: []byte(`{}`)})
	if err == nil || !strings.Contains(err.Error(), "502") {
		t.Fatalf("error = %v, want 502", err)
	}
}
