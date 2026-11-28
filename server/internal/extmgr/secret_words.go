package extmgr

import (
	"cmp"
	"context"
	"encoding/json"
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/extension"
)

// References' words (secrets.describe: "item › field", the vault), kept from the first time the provider gave them
// and sealed in the database (describedKey, which the key rotation re-seals: db.SealedSettings), so a settings page, a
// card or a restart of the manager never asks the provider again: asking 1Password's op on a computer for them would
// have the user authorize it each time its authorization lapsed (10 idle minutes, 12 hours, the app locking). They go
// with the provider (removed, switched off, another claiming the scheme); a secret the provider stored makes them stale:
// the next describe asks again, and the kept words answer while the provider cannot.

// describedKey is the setting with the sealed words (storedWords).
const describedKey = "secrets.described"

// wordsMost is how many references' words are kept; past it the oldest go.
const wordsMost = 500

// storedWords is one reference's kept words as the database has them: the reference and the words sealed together.
type storedWords struct {
	Provider    string    `json:"provider"`
	At          time.Time `json:"at"`
	Stale       bool      `json:"stale,omitempty"`
	WordsSealed string    `json:"words_sealed"`
}

// wordsPlain is what storedWords.WordsSealed opens to.
type wordsPlain struct {
	Reference    string `json:"reference"`
	ProviderName string `json:"provider_name,omitempty"`
	Title        string `json:"title"`
	Detail       string `json:"detail,omitempty"`
}

// loadWords reads the sealed words once; a read that fails is tried again at the next describe.
func (ps *extensions) loadWords(ctx context.Context) {
	w := &ps.words
	w.mu.Lock()
	loaded := w.loaded
	w.mu.Unlock()
	if loaded || ps.h == nil {
		return
	}
	var list []storedWords
	if ps.h.store != nil {
		if _, err := ps.h.store.Setting(ctx, describedKey, &list); err != nil {
			ps.logf("secret references: the kept words were not read: %v", err)
			return
		}
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.loaded {
		return
	}
	w.loaded = true
	if w.kept == nil {
		w.kept = map[string]keptWords{}
	}
	for _, e := range list {
		raw, err := ps.h.openKey(e.WordsSealed)
		if err != nil {
			continue
		}
		var p wordsPlain
		if json.Unmarshal([]byte(raw), &p) != nil || p.Reference == "" || p.Title == "" || e.Provider == "" {
			continue
		}
		if _, ok := w.kept[p.Reference]; !ok {
			w.kept[p.Reference] = keptWords{d: SecretDescription{Provider: e.Provider, ProviderName: p.ProviderName,
				Title: p.Title, Detail: p.Detail}, at: e.At, stale: e.Stale}
		}
	}
}

// saveWords writes the kept words, sealed, as they are now (the newest wordsMost).
func (ps *extensions) saveWords(ctx context.Context) {
	if ps.h == nil || ps.h.store == nil {
		return
	}
	w := &ps.words
	w.saveMu.Lock()
	defer w.saveMu.Unlock()
	w.mu.Lock()
	if !w.loaded {
		w.mu.Unlock()
		return // what the database has was never read: writing now would lose it
	}
	type entry struct {
		ref string
		k   keptWords
	}
	snap := make([]entry, 0, len(w.kept))
	for ref, k := range w.kept {
		snap = append(snap, entry{ref, k})
	}
	w.mu.Unlock()
	slices.SortFunc(snap, func(a, b entry) int { return cmp.Or(b.k.at.Compare(a.k.at), strings.Compare(a.ref, b.ref)) })
	if len(snap) > wordsMost {
		snap = snap[:wordsMost]
	}
	list := make([]storedWords, 0, len(snap))
	for _, e := range snap {
		raw, _ := json.Marshal(wordsPlain{Reference: e.ref, ProviderName: e.k.d.ProviderName, Title: e.k.d.Title, Detail: e.k.d.Detail})
		sealed, err := ps.h.sealKey(string(raw))
		if err != nil {
			ps.logf("secret references: the kept words were not sealed")
			return
		}
		list = append(list, storedWords{Provider: e.k.d.Provider, At: e.k.at, Stale: e.k.stale, WordsSealed: sealed})
	}
	wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if err := ps.h.store.PutSetting(wctx, describedKey, list); err != nil {
		ps.logf("secret references: the kept words were not saved: %v", err)
	}
}

// keepWords keeps reference's words d (and saves them) unless they were forgotten since gen.
func (ps *extensions) keepWords(ctx context.Context, reference string, d SecretDescription, gen uint64) {
	w := &ps.words
	w.mu.Lock()
	if gen != w.gen {
		w.mu.Unlock()
		return
	}
	if w.kept == nil {
		w.kept = map[string]keptWords{}
	}
	w.kept[reference] = keptWords{d: d, at: time.Now().UTC()}
	w.mu.Unlock()
	ps.saveWords(ctx)
}

// editWords runs fn on every reference's words; one it returns false for is dropped. Saves when any changed. The
// generation moves (words described meanwhile are not kept) when revoking or when any is dropped, as editGen does.
func (ps *extensions) editWords(ctx context.Context, revoking bool, fn func(ref string, k *keptWords) bool) {
	ps.loadWords(ctx)
	w := &ps.words
	w.mu.Lock()
	changed, dropped := false, false
	for ref, k := range w.kept {
		was := k
		if !fn(ref, &k) {
			delete(w.kept, ref)
			changed, dropped = true, true
		} else if k != was {
			w.kept[ref] = k
			changed = true
		}
	}
	if revoking || dropped {
		w.gen++
	}
	w.mu.Unlock()
	if changed {
		ps.saveWords(ctx)
	}
}

// wordsProviderChanged is a registry change of extension id as the kept words take it, as the kept values do
// (keptProviderChanged): removed, switched off or no longer claiming a reference's scheme drops its words; an upgrade,
// a restart or a new configuration keeps them; a rename follows it.
func (ps *extensions) wordsProviderChanged(ctx context.Context, c extension.Change) {
	old := ""
	if c.Old != nil && c.Old.Record.ID != c.ID {
		old = c.Old.Record.ID
	}
	var p *extension.Extension
	if c.Kind != "removed" {
		p = ps.reg.Snapshot(c.ID)
	}
	revoking := c.Kind == "removed" || old != "" || p == nil || !p.Record.Enabled
	ps.editWords(ctx, revoking, func(ref string, k *keptWords) bool {
		if k.d.Provider != c.ID && (old == "" || k.d.Provider != old) {
			return true
		}
		switch {
		case p == nil || !p.Record.Enabled:
			return false
		case p.Manifest != nil:
			scheme, ok := extension.ReferenceScheme(ref)
			if !ok || !slices.Contains(p.Manifest.Schemes(), scheme) {
				return false
			}
		}
		k.d.Provider = c.ID
		return true
	})
}

// staleWords: provider stored a secret (secrets.store): its references may name other things now.
func (ps *extensions) staleWords(ctx context.Context, provider string) {
	ps.editWords(ctx, true, func(_ string, k *keptWords) bool {
		if k.d.Provider == provider {
			k.stale = true
		}
		return true
	})
}
