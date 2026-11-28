package extmgr

// The text_lookup extension point (docs/extensions/extensions.md "Looking up a word"): the apps ask the first enabled
// extension with an enabled text_lookup contribution for the meaning of a word the user taps, and to remember or
// forget one. Any of the user's devices may call these, the same as the manager's other app endpoints.

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/nwverse/persona/sdk/go/wire"
	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/extwire"
	"github.com/nwverse/persona/server/internal/lazyre"
)

// textLookupTimeout bounds one call to the dictionary: the apps wait on a tap.
const textLookupTimeout = 3 * time.Second

// textLookupWordsMost is how many saved words one answer carries.
const textLookupWordsMost = 5000

// textLookupLetters are the Latin letters a word may hold, the same set as the apps (TextLookupWords.swift, word.ts):
// ASCII, Latin-1 and Latin Extended-A/B (é, ß, ł…), not × or ÷.
const textLookupLetters = `A-Za-z\x{00C0}-\x{00D6}\x{00D8}-\x{00F6}\x{00F8}-\x{024F}`

// textLookupWord is a word the apps may look up: Latin letters, with an apostrophe or a hyphen only between two
// letters ("don't", "well-known", "café"). textLookupWordMost bounds its length in characters, not bytes.
var textLookupWord = lazyre.New(`^[` + textLookupLetters + `]+(?:['-][` + textLookupLetters + `]+)*$`)

const textLookupWordMost = 64

const textLookupUnavailable = "text_lookup_unavailable"

// The text_lookup protocol's methods (schema/extension/protocol.schema.json).
const (
	textLookupStateMethod  = "text_lookup.state"
	textLookupLookupMethod = "text_lookup.lookup"
	textLookupSaveMethod   = "text_lookup.save"
	textLookupRemoveMethod = "text_lookup.remove"
)

// textLookupProvider is the dictionary the apps ask: the first enabled, compatible extension with an enabled
// text_lookup contribution, by extension id then contribution id.
type textLookupProvider struct {
	extension, contribution, name string
}

func (h *Manager) textLookupRoutes(api *http.ServeMux) {
	api.HandleFunc("GET /api/v1/text-lookup", h.getTextLookup)
	api.HandleFunc("POST /api/v1/text-lookup/lookup", h.postTextLookupLookup)
	api.HandleFunc("POST /api/v1/text-lookup/words", h.postTextLookupWord)
	api.HandleFunc("DELETE /api/v1/text-lookup/words/{word}", h.deleteTextLookupWord)
}

// textLookupProviders lists the dictionaries that are on, in the order the apps pick them.
func (ps *extensions) textLookupProviders() []textLookupProvider {
	if ps == nil || ps.reg == nil {
		return nil
	}
	var out []textLookupProvider
	for _, p := range ps.reg.List() {
		if !p.Record.Enabled || p.Problem != "" || p.Manifest == nil {
			continue
		}
		for _, it := range p.Manifest.TextLookups() {
			out = append(out, textLookupProvider{extension: p.Record.ID, contribution: it.ID.String(), name: it.Name})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].extension != out[j].extension {
			return out[i].extension < out[j].extension
		}
		return out[i].contribution < out[j].contribution
	})
	return out
}

// textLookup picks the dictionary the apps ask, and whether its program is running.
func (ps *extensions) textLookup() (textLookupProvider, *hubExtension, bool) {
	for _, r := range ps.textLookupProviders() {
		hp := ps.get(r.extension)
		if hp != nil && hp.proc != nil && hp.proc.Ready() {
			return r, hp, true
		}
	}
	return textLookupProvider{}, nil, false
}

// textLookupState is what GET /api/v1/text-lookup answers. available false is a fine answer: no dictionary is on, or
// the one that is did not answer.
type textLookupState struct {
	Available      bool     `json:"available"`
	Extension      string   `json:"extension,omitempty"`
	Contribution   string   `json:"contribution,omitempty"`
	Name           string   `json:"name,omitempty"`
	UnderlineSaved bool     `json:"underline_saved"`
	Words          []string `json:"words"`
}

func (h *Manager) getTextLookup(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	empty := textLookupState{Words: []string{}}
	r0, hp, ok := h.extensions.textLookup()
	if !ok {
		writeJSON(w, empty)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), textLookupTimeout)
	defer cancel()
	var res wire.TextLookupStateResult
	err := hp.proc.Call(ctx, textLookupStateMethod, wire.TextLookupStateParams{Contribution: r0.contribution}, &res)
	if err != nil {
		h.extensions.logf("plugin %s: text_lookup.state %s: %v", r0.extension, r0.contribution, err)
		writeJSON(w, empty)
		return
	}
	words := res.Words
	if words == nil {
		words = []string{}
	}
	if len(words) > textLookupWordsMost {
		words = words[:textLookupWordsMost]
	}
	writeJSON(w, textLookupState{Available: true, Extension: r0.extension, Contribution: r0.contribution,
		Name: r0.name, UnderlineSaved: res.UnderlineSaved, Words: words})
}

type textLookupBody struct {
	Word    string `json:"word"`
	Context string `json:"context"`
	Source  string `json:"source"`
}

// textLookupIn checks a lookup's word and trims its context to 500 characters.
func textLookupIn(b textLookupBody) (word, around string, ok bool) {
	word = strings.TrimSpace(b.Word)
	if utf8.RuneCountInString(word) > textLookupWordMost || !textLookupWord.MatchString(word) {
		return "", "", false
	}
	around = strings.TrimSpace(b.Context)
	if utf8.RuneCountInString(around) > 500 {
		runes := []rune(around)
		around = string(runes[:500])
	}
	return word, around, true
}

func (h *Manager) postTextLookupLookup(w http.ResponseWriter, r *http.Request) {
	var b textLookupBody
	if err := decodeJSONBody(w, r, &b, 8<<10); err != nil {
		writeBodyErr(w, err, errors.New("a JSON body with word"))
		return
	}
	word, around, ok := textLookupIn(b)
	if !ok {
		writeErr(w, http.StatusBadRequest, errors.New("word: letters, an apostrophe or a hyphen, 1 to 64"))
		return
	}
	r0, hp, ready := h.extensions.textLookup()
	if !ready {
		writeErrCode(w, http.StatusNotFound, textLookupUnavailable, errors.New("no dictionary is available"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), textLookupTimeout)
	defer cancel()
	var res wire.TextLookupLookupResult
	err := hp.proc.Call(ctx, textLookupLookupMethod, wire.TextLookupLookupParams{
		Contribution: r0.contribution, Word: word, Context: around, Lang: "en", UILang: string(h.requestLang(r)),
	}, &res)
	if err != nil {
		h.extensions.logf("plugin %s: text_lookup.lookup %s: %v", r0.extension, r0.contribution, err)
		writeErrCode(w, http.StatusNotFound, textLookupUnavailable, errors.New("no dictionary is available"))
		return
	}
	if res.Senses == nil {
		res.Senses = []wire.TextLookupSense{}
	}
	writeJSON(w, res)
}

func (h *Manager) postTextLookupWord(w http.ResponseWriter, r *http.Request) {
	h.textLookupChange(w, r, true)
}

func (h *Manager) deleteTextLookupWord(w http.ResponseWriter, r *http.Request) {
	h.textLookupChange(w, r, false)
}

// textLookupChange saves or forgets a word, then tells the apps the dictionary changed.
func (h *Manager) textLookupChange(w http.ResponseWriter, r *http.Request, save bool) {
	var b textLookupBody
	if save {
		if err := decodeJSONBody(w, r, &b, 8<<10); err != nil {
			writeBodyErr(w, err, errors.New("a JSON body with word"))
			return
		}
	} else {
		b.Word = r.PathValue("word")
	}
	word, around, ok := textLookupIn(b)
	if !ok {
		writeErr(w, http.StatusBadRequest, errors.New("word: letters, an apostrophe or a hyphen, 1 to 64"))
		return
	}
	r0, hp, ready := h.extensions.textLookup()
	if !ready {
		writeErrCode(w, http.StatusNotFound, textLookupUnavailable, errors.New("no dictionary is available"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), textLookupTimeout)
	defer cancel()
	var res wire.TextLookupSavedResult
	var err error
	if save {
		err = hp.proc.Call(ctx, textLookupSaveMethod, wire.TextLookupSaveParams{
			Contribution: r0.contribution, Word: word, Context: around, Source: b.Source,
		}, &res)
	} else {
		err = hp.proc.Call(ctx, textLookupRemoveMethod, wire.TextLookupRemoveParams{Contribution: r0.contribution, Word: word}, &res)
	}
	if err != nil {
		h.extensions.logf("plugin %s: text_lookup %s: %v", r0.extension, r0.contribution, err)
		writeErrCode(w, http.StatusNotFound, textLookupUnavailable, errors.New("no dictionary is available"))
		return
	}
	h.textLookupChanged()
	writeJSON(w, res)
}

// textLookupChanged tells the apps to ask GET /api/v1/text-lookup again (docs/extensions/extensions.md "Looking up a word").
func (h *Manager) textLookupChanged() {
	h.broadcast(extwire.TextLookupChanged{Type: extwire.EventTextLookupChanged})
}

// textLookupTouched says whether a registry change can move which dictionary the apps ask, or whether one is there.
// The extension after the change is read by the caller: Old is the only side a Change carries.
func textLookupTouched(old *extension.Extension, id string, reg *extension.Registry) bool {
	has := func(p *extension.Extension) bool {
		return p != nil && p.Full != nil && len(p.Full.Items("text_lookup")) > 0
	}
	return has(old) || has(reg.Get(id))
}
