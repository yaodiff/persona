//go:build !windows

package assistant

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"os/user"
	"path/filepath"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistant/kblark"
	"github.com/nwverse/persona/server/internal/cliport"
	"github.com/nwverse/persona/server/internal/cliwire"
	"github.com/nwverse/persona/server/internal/kb"
)

// TestRealLarkImportShowsOnTheRoutes imports through the real lark-cli into a temporary knowledge base, then reads it
// back through GET /api/v1/kb/meetings and /api/v1/kb/docs, the routes the App uses. It never touches ~/.persona/data
// (it only copies the owner's document files in) and is skipped unless KB_LARK_REAL=1.
func TestRealLarkImportShowsOnTheRoutes(t *testing.T) {
	if os.Getenv("KB_LARK_REAL") != "1" {
		t.Skip("set KB_LARK_REAL=1 to run against the real lark-cli")
	}
	needsLarkCLI(t)
	// testhome gives the package a throw-away HOME; lark-cli's login is under the owner's real one, which is set
	// only for the import below.
	home := ""
	if u, err := user.Current(); err == nil {
		home = u.HomeDir
	}
	f := newFakeHub()
	s := newKBService(t, f)
	e := kbEng(t, s)

	// The owner's documents as they are, and a copy of one with its title blanked: the import must bring it back.
	if home != "" {
		entries, _ := os.ReadDir(filepath.Join(home, ".persona", "data", "docs"))
		blanked := false
		for _, ent := range entries {
			if ent.IsDir() || filepath.Ext(ent.Name()) != ".json" {
				continue
			}
			raw, err := os.ReadFile(filepath.Join(home, ".persona", "data", "docs", ent.Name()))
			if err != nil {
				continue
			}
			var it kb.Item
			if json.Unmarshal(raw, &it) != nil {
				continue
			}
			if !blanked {
				it.Title = ""
				blanked = true
			}
			if err := e.Store.PutItem(&it); err != nil {
				t.Fatal(err)
			}
		}
	}

	if home != "" {
		t.Setenv("HOME", home)
	}
	client := kblark.NewClient(cliport.NewRunner(cliwire.Config{Bin: os.Getenv("KB_LARK_BIN"), Timeout: time.Minute}), e.Store.Zone())
	run := func() kblark.Result {
		res, err := kblark.Run(context.Background(), e, client, kblark.Config{}, time.Now(), t.Logf)
		if err != nil {
			t.Fatalf("the import failed: %v", err)
		}
		return res
	}
	first := run()
	if first.Minutes == 0 {
		t.Skip("no minutes in the window")
	}
	meetings := len(e.Store.Items(kb.KindMeeting))
	second := run()
	if n := len(e.Store.Items(kb.KindMeeting)); n != meetings {
		t.Fatalf("%d minutes after two runs, %d after the first", n, meetings)
	}
	if second.Changed != 0 {
		t.Fatalf("the second run changed %d item(s)", second.Changed)
	}

	type entry struct {
		Kind    string    `json:"kind"`
		ID      string    `json:"id"`
		Name    string    `json:"name"`
		OneLine string    `json:"one_line"`
		Updated time.Time `json:"updated"`
		Extra   string    `json:"extra"`
	}
	get := func(kind string) []entry {
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, httptest.NewRequest("GET", "/api/v1/kb/"+kind, nil))
		if rec.Code != 200 {
			t.Fatalf("GET /api/v1/kb/%s: %d %s", kind, rec.Code, rec.Body.String())
		}
		var out struct {
			Items []entry `json:"items"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("GET /api/v1/kb/%s: %v\n%s", kind, err, rec.Body.String())
		}
		return out.Items
	}
	meetingList := get("meetings")
	if len(meetingList) != meetings {
		t.Fatalf("GET /api/v1/kb/meetings: %d item(s), %d stored", len(meetingList), meetings)
	}
	shown := false
	for _, m := range meetingList {
		t.Logf("meeting: %s | %s | %s", m.Name, m.Updated.Format("2006-01-02 15:04"), m.Extra)
		if m.Name != "" && !m.Updated.IsZero() && m.Extra != "" && m.OneLine != "" {
			shown = true
		}
	}
	if !shown {
		t.Fatal("no meeting had a title, time, link and summary")
	}
	docList := get("docs")
	titled := 0
	for _, d := range docList {
		t.Logf("doc: %s | %s", d.Name, d.Extra)
		if d.Name != "" {
			titled++
		}
	}
	if first.Filled == 0 || titled == 0 {
		t.Fatalf("no document title was filled (%d filled, %d titled on the route)", first.Filled, titled)
	}
}
