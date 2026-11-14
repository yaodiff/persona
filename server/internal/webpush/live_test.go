package webpush

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"testing"
	"time"
)

// TestLiveDelivery sends one push to a real push service, for web/e2e/web-push.mjs --live: PERSONA_WEBPUSH_LIVE names
// a JSON file with the VAPID key pair a browser subscribed with, its subscription and the message. Skipped without it:
// it reaches the internet.
func TestLiveDelivery(t *testing.T) {
	path := os.Getenv("PERSONA_WEBPUSH_LIVE")
	if path == "" {
		t.Skip("PERSONA_WEBPUSH_LIVE not set (web/e2e/web-push.mjs --live sets it)")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var in struct {
		VAPID        Keys         `json:"vapid"`
		Subscription Subscription `json:"subscription"`
		Message      string       `json:"message"`
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		t.Fatal(err)
	}
	if _, err := CheckEndpoint(in.Subscription.Endpoint); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	req, err := NewRequest(ctx, in.Subscription, in.VAPID, "https://github.com/nwverse/persona", []byte(in.Message),
		Message{TTL: time.Minute, Urgency: UrgencyHigh, Topic: TopicFor("live")}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
	if resp.StatusCode/100 != 2 {
		t.Fatalf("%s answered %d: %s", Service(req.URL.Hostname()), resp.StatusCode, body)
	}
	t.Logf("%s took it: %d", Service(req.URL.Hostname()), resp.StatusCode)
}
