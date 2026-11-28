package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
)

func readInboxStoredSources(t *testing.T, h *inboxHub, query string) (*assistantwire.InboxStoredSources, int) {
	t.Helper()
	w := httptest.NewRecorder()
	h.Service.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/inbox"+query, nil))
	if w.Code != http.StatusOK {
		t.Fatalf("GET %s: %d %s", query, w.Code, w.Body.String())
	}
	var out struct {
		StoredSources *assistantwire.InboxStoredSources `json:"stored_sources"`
		Items         []json.RawMessage                 `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out.StoredSources, len(out.Items)
}

func TestInboxStoredSourcesCoverAllRetainedHistory(t *testing.T) {
	h := newInboxHub(t)
	old := time.Now().Add(-30 * 24 * time.Hour)
	if err := h.st.PutInboxItem(context.Background(), "larkcli:old", []byte(`{"id":"larkcli:old","source":"larkcli","kind":"dm"}`), old, &old); err != nil {
		t.Fatal(err)
	}
	if err := h.inboxReady(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(h.inbox.items) != 0 {
		t.Fatal("the completed item older than inboxForget is unexpectedly in memory")
	}
	stored, listed := readInboxStoredSources(t, h, "?sources=all")
	if listed != 0 || stored == nil || !stored.Lark || stored.Mail {
		t.Fatalf("listed %d, stored_sources %+v; want no listed items and Lark only", listed, stored)
	}

	now := time.Now()
	mail := &InboxItem{ID: "mail:open", Source: "mail", Kind: KindMail, UpdatedAt: now}
	h.inbox.mu.Lock()
	h.inbox.items[mail.ID] = mail
	h.inbox.mu.Unlock()
	h.saveInbox(context.Background(), []*InboxItem{mail})
	stored, _ = readInboxStoredSources(t, h, "?sources=all")
	if stored == nil || !stored.Lark || !stored.Mail {
		t.Fatalf("stored_sources %+v, want Lark and mail", stored)
	}

	for _, query := range []string{"?sources=all&since=0", "?sources=all&since=0&chain=last"} {
		stored, _ = readInboxStoredSources(t, h, query)
		if stored == nil || !stored.Lark || !stored.Mail {
			t.Fatalf("%s: stored_sources %+v, want Lark and mail", query, stored)
		}
	}
}

func TestInboxStoredSourcesEmptyAndUnknown(t *testing.T) {
	h := newInboxHub(t)
	if err := h.inboxReady(context.Background()); err != nil {
		t.Fatal(err)
	}
	stored, _ := readInboxStoredSources(t, h, "")
	if stored == nil || stored.Lark || stored.Mail {
		t.Fatalf("empty hub: stored_sources %+v, want known false/false", stored)
	}

	h.st.fail = errors.New("synthetic store failure")
	stored, _ = readInboxStoredSources(t, h, "")
	if stored != nil {
		t.Fatalf("failed source read: stored_sources %+v, want absent (unknown)", stored)
	}
}
