package transcript

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/nwverse/persona/server/internal/proto"
)

func TestRelocateConcurrentWithFullAndWithImages(t *testing.T) {
	dir := t.TempDir()
	a, b := filepath.Join(dir, "a.jsonl"), filepath.Join(dir, "b.jsonl")

	longText := strings.Repeat("transcript line ", 2500) + "end"
	imgData := strings.Repeat("AAAA", 6000)
	body := fmt.Sprintf(
		`{"type":"user","uuid":"u-long","timestamp":"2026-11-10T10:00:00Z","message":{"role":"user","content":%q}}`+"\n"+
			`{"type":"user","uuid":"u-img","parentUuid":"u-long","timestamp":"2026-11-10T10:00:01Z","message":{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":%q}}]}}`+"\n",
		longText, imgData,
	)
	for _, p := range []string{a, b} {
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	f := NewFile(a)
	if _, err := f.Refresh(); err != nil {
		t.Fatal(err)
	}
	items, _, _ := f.Page(0, 10)
	var longItem, imgItem proto.Item
	for _, it := range items {
		if it.FullSize > 0 {
			longItem = it
		}
		if it.Image != nil && it.Image.Lazy {
			imgItem = it
		}
	}
	if longItem.ID == "" {
		t.Fatalf("expected a cut text item in fixture, got %+v", items)
	}
	if imgItem.ID == "" {
		t.Fatalf("expected a lazy image item in fixture, got %+v", items)
	}

	t.Run("Full", func(t *testing.T) {
		start := make(chan struct{})
		var done atomic.Bool
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			for i := 0; !done.Load(); i++ {
				if i%2 == 0 {
					f.Relocate(b)
				} else {
					f.Relocate(a)
				}
			}
		}()
		var fullErr error
		go func() {
			defer wg.Done()
			defer done.Store(true)
			<-start
			for range 200 {
				got, err := f.Full(longItem.ID)
				if err != nil {
					fullErr = err
					return
				}
				if got.Text != longText || got.FullSize != 0 {
					fullErr = fmt.Errorf("unexpected full item: len=%d fullSize=%d", len(got.Text), got.FullSize)
					return
				}
			}
		}()
		close(start)
		wg.Wait()
		if fullErr != nil {
			t.Fatal(fullErr)
		}
	})

	t.Run("WithImages", func(t *testing.T) {
		start := make(chan struct{})
		var done atomic.Bool
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			for i := 0; !done.Load(); i++ {
				if i%2 == 0 {
					f.Relocate(b)
				} else {
					f.Relocate(a)
				}
			}
		}()
		var imgErr error
		go func() {
			defer wg.Done()
			defer done.Store(true)
			<-start
			in := []proto.Item{imgItem}
			for range 200 {
				got := f.WithImages(in)
				if len(got) != 1 || got[0].Image == nil || got[0].Image.Lazy || got[0].Image.Data != imgData {
					imgErr = fmt.Errorf("unexpected WithImages result: %+v", got)
					return
				}
			}
		}()
		close(start)
		wg.Wait()
		if imgErr != nil {
			t.Fatal(imgErr)
		}
	})
}
