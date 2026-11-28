package hub

import (
	"errors"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/apns"
	"github.com/nwverse/persona/server/internal/assistantwire"
)

// Invalid APNs device/Live Activity/VoIP tokens must fail immediately without retrying
// (avoiding multi-second sleeps under sendMu.RLock) and map to pushRefused rather than pushUnknown.
func TestDirectPushAndRingDoNotRetryInvalidDeviceToken(t *testing.T) {
	f := &flakyAPNs{}
	h := directHub(t, f)
	// Huge retry waits prove that any retry attempt would immediately stall past the test bound.
	h.push.retryWaits = []time.Duration{time.Hour, time.Hour}
	d := Device{DeviceID: "phone", Name: "phone", APNSToken: "devtok", VoIPToken: "bad;voip", Topic: "app.example"}
	h.push.register(d)

	directDone := make(chan error, 1)
	go func() {
		_, err := h.push.sendDirect(d, apns.Request{
			Token:    "../bad",
			PushType: "alert",
			Priority: "10",
			Body:     []byte(`{"aps":{}}`),
		})
		directDone <- err
	}()
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	select {
	case err := <-directDone:
		if !errors.Is(err, apns.ErrInvalidDeviceToken) {
			t.Fatalf("sendDirect error = %v, want ErrInvalidDeviceToken", err)
		}
		if got := sendFailure(err); got != pushRefused {
			t.Fatalf("sendFailure(ErrInvalidDeviceToken) = %v, want pushRefused", got)
		}
	case <-timer.C:
		t.Fatal("sendDirect retried on ErrInvalidDeviceToken instead of breaking immediately")
	}

	outcome := make(chan pushResult, 1)
	laDone := h.push.sendLiveActivityThen(d, "bad;token", map[string]any{"aps": map[string]any{"event": "update"}}, "10", func() {}, func(o pushResult) {
		outcome <- o
	})
	laTimer := time.NewTimer(5 * time.Second)
	defer laTimer.Stop()
	select {
	case <-laDone:
	case <-laTimer.C:
		t.Fatal("sendLiveActivityThen retried on invalid Live Activity token")
	}
	if got := <-outcome; got != pushRefused {
		t.Fatalf("sendLiveActivityThen outcome = %v, want pushRefused", got)
	}

	ringDone := make(chan error, 1)
	go func() {
		ringDone <- h.push.ringOne(d, map[string]any{"kind": "call"}, assistantwire.CallRing{ID: "c1"})
	}()
	ringTimer := time.NewTimer(5 * time.Second)
	defer ringTimer.Stop()
	select {
	case err := <-ringDone:
		if !errors.Is(err, apns.ErrInvalidDeviceToken) {
			t.Fatalf("ringOne error = %v, want ErrInvalidDeviceToken", err)
		}
	case <-ringTimer.C:
		t.Fatal("ringOne retried on ErrInvalidDeviceToken instead of breaking immediately")
	}

	if n := f.count(); n != 0 {
		t.Fatalf("invalid tokens produced %d outbound APNs requests, want 0", n)
	}
}
