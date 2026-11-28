package extmgr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/sdk/go/wire"
	"github.com/nwverse/persona/server/internal/extension"
	pserve "github.com/nwverse/persona/server/internal/extension/serve"
)

// serveTextLookupExtensionForTest is a tiny dictionary: context, deploy and latency. LOOKUP_CASE=fail makes state
// fail, hang makes lookup wait, slow makes state wait. Saved words live in memory for the process.
func serveTextLookupExtensionForTest() {
	saved := map[string]bool{}
	_ = pserve.Serve(context.Background(), os.Stdin, os.Stdout, pserve.Program{ID: os.Getenv("LOOKUP_ID"), Version: "1.0.0",
		Namespaces: map[string]pserve.Handler{
			"text_lookup": func(ctx context.Context, method string, params json.RawMessage) (any, error) {
				switch os.Getenv("LOOKUP_CASE") {
				case "fail":
					if method == "state" {
						return nil, errors.New("the dictionary is down")
					}
				case "hang":
					if method == "lookup" {
						<-ctx.Done()
						return nil, ctx.Err()
					}
				case "slow":
					if method == "state" {
						<-ctx.Done()
						return nil, ctx.Err()
					}
				}
				switch method {
				case "state":
					words := make([]string, 0, len(saved))
					for w := range saved {
						words = append(words, w)
					}
					extra := 0
					fmt.Sscan(os.Getenv("LOOKUP_EXTRA"), &extra)
					for i := 0; i < extra; i++ {
						words = append(words, fmt.Sprintf("w%d", i))
					}
					return wire.TextLookupStateResult{UnderlineSaved: true, Words: words}, nil
				case "lookup":
					var p wire.TextLookupLookupParams
					_ = json.Unmarshal(params, &p)
					if os.Getenv("LOOKUP_OUT") != "" {
						_ = os.WriteFile(os.Getenv("LOOKUP_OUT"), params, 0o600)
					}
					entry, known := map[string]wire.TextLookupLookupResult{
						"context": {Found: true, Word: "context", Lemma: "context", Phonetic: "/\u02c8k\u0252ntekst/",
							Senses: []wire.TextLookupSense{{Pos: "noun", Text: "the words around a word"}}},
						"deploy": {Found: true, Word: "deploy", Lemma: "deploy",
							Senses: []wire.TextLookupSense{{Pos: "verb", Text: "to put into use"}}},
						"latency": {Found: true, Word: "latency", Lemma: "latency",
							Senses: []wire.TextLookupSense{{Pos: "noun", Text: "the time a thing takes"}}},
					}[strings.ToLower(p.Word)]
					if !known {
						return wire.TextLookupLookupResult{Found: false, Word: p.Word}, nil
					}
					entry.Saved = saved[strings.ToLower(p.Word)]
					return entry, nil
				case "save":
					var p wire.TextLookupSaveParams
					_ = json.Unmarshal(params, &p)
					saved[strings.ToLower(p.Word)] = true
					return wire.TextLookupSavedResult{Saved: true}, nil
				case "remove":
					var p wire.TextLookupRemoveParams
					_ = json.Unmarshal(params, &p)
					delete(saved, strings.ToLower(p.Word))
					return wire.TextLookupSavedResult{Saved: false}, nil
				}
				return nil, pserve.UnknownMethod(method)
			}}})
}

func textLookupPackage(t *testing.T, id string, env map[string]string) string {
	dir := t.TempDir()
	self, _ := os.Executable()
	_ = os.MkdirAll(filepath.Join(dir, "bin"), 0o755)
	env["PERSONA_TEST_PLUGIN"] = "lookup"
	env["LOOKUP_ID"] = id
	var sets []string
	for k, v := range env {
		sets = append(sets, k+"="+v)
	}
	script := "#!/bin/sh\n" + strings.Join(sets, " ") + " exec " + self + " \"$@\"\n"
	_ = os.WriteFile(filepath.Join(dir, "bin", "dict"), []byte(script), 0o755)
	manifest := fmt.Sprintf(`{"manifest_version": 1, "id": %q, "name": "Example Dictionary", "version": "1.0.0",
		"engines": {"plugin_protocol": 1}, "runtime": {"command": {%q: "bin/dict"}},
		"activation_events": ["on_startup"],
		"contributes": {"text_lookup": [{"id": "dict", "name": "Example Dictionary", "languages": ["en"]}]}}`,
		id, runtime.GOOS+"-"+runtime.GOARCH)
	_ = os.WriteFile(filepath.Join(dir, extension.ManifestFile), []byte(manifest), 0o644)
	return dir
}

func installTextLookup(t *testing.T, mux http.Handler, token, dir string) {
	t.Helper()
	_, pv := call(t, mux, "POST", "/api/v1/plugins/-/preview", token, `{"source": "dir:`+dir+`"}`)
	code, _ := call(t, mux, "POST", "/api/v1/plugins/-/install", token,
		fmt.Sprintf(`{"preview_id": %q, "permissions_digest": %q}`, pv["preview_id"], pv["permissions_digest"]))
	if code != 200 {
		t.Fatalf("install: %d %v", code, pv)
	}
}

func TestTextLookup(t *testing.T) {
	h, _ := extensionTestHub(t)
	mux := h.routes()
	master := h.cfg.Token

	code, body := call(t, mux, "GET", "/api/v1/text-lookup", master, "")
	if code != 200 || body["available"] != false {
		t.Fatalf("none installed: 200 available false: %d %v", code, body)
	}
	code, body = call(t, mux, "POST", "/api/v1/text-lookup/lookup", master, `{"word": "context"}`)
	if code != 404 || body["code"] != "text_lookup_unavailable" {
		t.Fatalf("none installed: %d %v", code, body)
	}

	out := filepath.Join(t.TempDir(), "lookup.json")
	installTextLookup(t, mux, master, textLookupPackage(t, "example-dict", map[string]string{"LOOKUP_OUT": out}))
	up := func() bool {
		hp := h.extensions.get("example-dict")
		return hp != nil && hp.proc != nil && hp.proc.Ready()
	}
	waitLong(t, "the dictionary up", up)

	code, body = call(t, mux, "GET", "/api/v1/text-lookup", master, "")
	if code != 200 || body["available"] != true || body["extension"] != "example-dict" || body["contribution"] != "dict" ||
		body["underline_saved"] != true {
		t.Fatalf("state: %d %v", code, body)
	}

	code, body = call(t, mux, "POST", "/api/v1/text-lookup/lookup", master, `{"word": " context ", "context": "the surrounding context"}`)
	if code != 200 || body["found"] != true || body["lemma"] != "context" {
		t.Fatalf("lookup: %d %v", code, body)
	}
	raw, _ := os.ReadFile(out)
	var got wire.TextLookupLookupParams
	_ = json.Unmarshal(raw, &got)
	if got.Word != "context" || got.Lang != "en" || got.Contribution != "dict" || got.UILang == "" {
		t.Fatalf("params: %+v", got)
	}

	code, body = call(t, mux, "POST", "/api/v1/text-lookup/words", master, `{"word": "deploy", "source": "chat"}`)
	if code != 200 || body["saved"] != true {
		t.Fatalf("save: %d %v", code, body)
	}
	code, body = call(t, mux, "DELETE", "/api/v1/text-lookup/words/deploy", master, "")
	if code != 200 || body["saved"] != false {
		t.Fatalf("remove: %d %v", code, body)
	}

	// Accented Latin words are words, the same as on iOS and the web: looked up, saved and removed (the path escaped).
	for _, word := range []string{"café", "résumé", "naïve", "Ångström", "well-known", "don't"} {
		code, body = call(t, mux, "POST", "/api/v1/text-lookup/lookup", master, fmt.Sprintf(`{"word": %q}`, word))
		if code != 200 || body["word"] != word {
			t.Errorf("lookup %q: %d %v", word, code, body)
		}
		code, body = call(t, mux, "POST", "/api/v1/text-lookup/words", master, fmt.Sprintf(`{"word": %q}`, word))
		if code != 200 || body["saved"] != true {
			t.Errorf("save %q: %d %v", word, code, body)
		}
	}
	code, body = call(t, mux, "GET", "/api/v1/text-lookup", master, "")
	if words, _ := body["words"].([]any); code != 200 || !slices.Contains(words, any("café")) || !slices.Contains(words, any("naïve")) {
		t.Fatalf("state after saving accented words: %d %v", code, body)
	}
	for _, word := range []string{"café", "résumé", "naïve"} {
		code, body = call(t, mux, "DELETE", "/api/v1/text-lookup/words/"+url.PathEscape(word), master, "")
		if code != 200 || body["saved"] != false {
			t.Errorf("remove %q: %d %v", word, code, body)
		}
	}

	for _, word := range []string{"", "context!", "123", "v2", "a--b", "-ab", "ab'", "单词", "слово", "a×b", "a÷b", "ab_cd", // i18n-ignore: non-Latin words must be refused
		strings.Repeat("a", 65), strings.Repeat("é", 65)} {
		code, body = call(t, mux, "POST", "/api/v1/text-lookup/lookup", master, fmt.Sprintf(`{"word": %q}`, word))
		if code != 400 {
			t.Errorf("word %q: %d %v", word, code, body)
		}
	}

	// A contribution switched off is no dictionary.
	if code, _ := call(t, mux, "PUT", "/api/v1/extensions/example-dict/-/contributions/text_lookup/dict", master, `{"enabled": false}`); code != 200 {
		t.Fatalf("switch off: %d", code)
	}
	code, body = call(t, mux, "GET", "/api/v1/text-lookup", master, "")
	if code != 200 || body["available"] != false {
		t.Fatalf("contribution off: %d %v", code, body)
	}
	code, body = call(t, mux, "POST", "/api/v1/text-lookup/lookup", master, `{"word": "context"}`)
	if code != 404 || body["code"] != "text_lookup_unavailable" {
		t.Fatalf("contribution off lookup: %d %v", code, body)
	}
}

func TestTextLookupTimeout(t *testing.T) {
	h, _ := extensionTestHub(t)
	mux := h.routes()
	master := h.cfg.Token
	installTextLookup(t, mux, master, textLookupPackage(t, "slow-dict", map[string]string{"LOOKUP_CASE": "hang"}))
	waitLong(t, "the slow dictionary up", func() bool {
		hp := h.extensions.get("slow-dict")
		return hp != nil && hp.proc != nil && hp.proc.Ready()
	})
	start := time.Now()
	code, body := call(t, mux, "POST", "/api/v1/text-lookup/lookup", master, `{"word": "context"}`)
	if code != 404 || body["code"] != "text_lookup_unavailable" {
		t.Fatalf("a hung lookup: %d %v", code, body)
	}
	if time.Since(start) > 15*time.Second {
		t.Fatalf("the lookup waited %s, past its timeout", time.Since(start))
	}
}
