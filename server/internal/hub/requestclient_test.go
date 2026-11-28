package hub

import (
	"bytes"
	"context"
	"errors"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRequestClientParsing(t *testing.T) {
	if got := requestClientOf(context.Background()); got != (requestClient{}) {
		t.Fatal(got)
	}
	for _, value := range []string{"android/1.2.3", "ios/1.0-beta+1", "watch/2"} {
		got := parseRequestClient(value)
		if got.Platform+"/"+got.Version != value {
			t.Fatalf("%q: %+v", value, got)
		}
	}
	for _, value := range []string{"", "android", "android/", "/1", "android/1/2", "android/1\nforged", "android/1 token=example", strings.Repeat("a", 129) + "/1"} {
		if got := parseRequestClient(value); got != (requestClient{}) {
			t.Fatalf("accepted %q: %+v", value, got)
		}
	}
	for _, value := range []string{"tab-1", "web.session_A+2", "018f2f62-6e4b-7a6f-b5c1-9c2f2d65a901"} {
		if got := parseRequestClientInstance(value); got != value {
			t.Fatalf("instance %q: %q", value, got)
		}
	}
	for _, value := range []string{"", "tab/1", "tab\nforged", "tab token=example", strings.Repeat("a", 129)} {
		if got := parseRequestClientInstance(value); got != "" {
			t.Fatalf("accepted instance %q: %q", value, got)
		}
	}
}

func TestRequestClientContextAndLogs(t *testing.T) {
	for _, path := range []string{"/api/v1/machines", "/api/v1/ws"} {
		t.Run(path, func(t *testing.T) {
			h := newTestHub(t)
			var logs bytes.Buffer
			h.logger = log.New(&logs, "", 0)
			handler := logRequests(h, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if got := requestClientOf(r.Context()); got.Platform != "android" || got.Version != "1.2" || got.Instance != "tab-1" {
					t.Fatalf("context: %+v", got)
				}
				writeErr(w, 401, errors.New("invalid token"))
			}))
			r := httptest.NewRequest("GET", path+"?token=example-secret", nil)
			r.Header.Set("X-Persona-Client", "android/1.2")
			r.Header.Set("X-Persona-Client-Instance", "tab-1")
			r.Header.Set("X-Client-Request-Id", "1234abcd")
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if got := logs.String(); !strings.Contains(got, `client="android/1.2"`) || strings.Contains(got, "example-secret") || !strings.Contains(got, "[1234abcd]") {
				t.Fatalf("log: %q", got)
			}
			if w.Header().Get("X-Request-Id") != "1234abcd" {
				t.Fatal(w.Header())
			}
		})
	}
}

func TestRequestClientLegacyLogs(t *testing.T) {
	h := newTestHub(t)
	var logs bytes.Buffer
	h.logger = log.New(&logs, "", 0)
	handler := logRequests(h, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := requestClientOf(r.Context()); got != (requestClient{}) {
			t.Fatal(got)
		}
		writeErr(w, 404, errors.New("not found"))
	}))
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/missing", nil))
	if strings.Contains(logs.String(), "client=") {
		t.Fatalf("legacy log changed: %s", logs.String())
	}
}
