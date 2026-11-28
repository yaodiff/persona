package extmgr

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/sdk/go/wire"
	"github.com/nwverse/persona/server/internal/extension"
)

// References saved in Settings (docs/extensions/secret-providers.md, "Launcher keys" and "Extension secrets"): a
// launcher's key is opened every time a computer pulls it and every time the manager calls the provider's service
// itself, and an extension's secret every time its program starts (a restart, an upgrade, a new setting). Saving the
// reference in Settings is the owner's standing approval for that launcher or extension, so the value its provider
// gave is kept here, in this process's memory and sealed with the manager's secret.key in the database
// (launcherKeptKey, which the key rotation re-seals: db.SealedSettings), and every later use is answered from it: a
// restart of the manager, a provider that is locked, restarting or offline, or one that would have to ask the user
// to authorize it again (1Password's op on a computer asks after 10 idle minutes, 12 hours, or the app locking) asks
// nobody. It is kept until it is revoked: the provider is removed or switched off or no longer claims the scheme, the
// launcher or extension is removed or its secret no longer is this reference, or the provider says the reference is
// gone (not_found) or refuses it (refused). A failure that may pass (not signed in, provider off, rate limited, a
// timeout) leaves it as it is. RefreshLauncherSecret asks the provider again (the secret was rotated). A value marked
// stale (the provider stored a secret) is asked for again quietly (SecretsResolveParams.Quiet: the provider must not
// ask the user for it) and keeps answering while it cannot be. An Agent's hand-off resolves after each card the user
// approves, and is never kept here.

// launcherKeptKey is the setting with the sealed copies (storedKeptSecret).
const launcherKeptKey = "secrets.launcher_kept"

// keptSecrets are resolved launcher keys, by reference and the use they were resolved for.
type keptSecrets struct {
	mu   sync.Mutex
	kept map[keptKey]keptSecret
	// gen counts the forgets: a value resolved while one happened is not kept (its provider may have just gone).
	gen uint64
	// loaded: the sealed copies were read from the database (loadKept); nothing is written before.
	loaded bool
	// saveMu puts the writes of the sealed copies one after another, each of the state when it took it.
	saveMu sync.Mutex
}

type keptKey struct{ reference, kind, id, machine string }

type keptSecret struct {
	value, provider string
	at              time.Time
	// stale: the provider may give another value now (a secret it stored): the next use asks it, and keeps answering
	// with this one while it cannot be asked.
	stale bool
}

// storedKeptSecret is one kept value as the database has it: the reference and the value are sealed together. A
// launcher's key has Launcher (and Machine); an extension's secret has Extension.
type storedKeptSecret struct {
	Launcher    string    `json:"launcher,omitempty"`
	Extension   string    `json:"extension,omitempty"`
	Machine     string    `json:"machine,omitempty"`
	Provider    string    `json:"provider"`
	At          time.Time `json:"at"`
	Stale       bool      `json:"stale,omitempty"`
	ValueSealed string    `json:"value_sealed"`
}

// keptPlain is what storedKeptSecret.ValueSealed opens to.
type keptPlain struct {
	Reference string `json:"reference"`
	Value     string `json:"value"`
}

// get is key's kept value and whether it is stale, or the generation to put a fresh one with.
func (k *keptSecrets) get(key keptKey) (value string, stale bool, gen uint64, ok bool) {
	k.mu.Lock()
	defer k.mu.Unlock()
	v, ok := k.kept[key]
	return v.value, v.stale, k.gen, ok
}

// put keeps value for key, and for the launcher's other uses of the same reference (one value per reference),
// unless something was forgotten since gen. False: nothing kept.
func (k *keptSecrets) put(key keptKey, value, provider string, gen uint64) bool {
	k.mu.Lock()
	defer k.mu.Unlock()
	if gen != k.gen {
		return false
	}
	if k.kept == nil {
		k.kept = map[keptKey]keptSecret{}
	}
	now := time.Now().UTC()
	for other := range k.kept {
		if other.reference == key.reference && other.kind == key.kind && other.id == key.id {
			k.kept[other] = keptSecret{value: value, provider: provider, at: now}
		}
	}
	k.kept[key] = keptSecret{value: value, provider: provider, at: now}
	return true
}

// edit runs fn on every kept value; one it returns false for is dropped. Whether any was dropped or changed.
func (k *keptSecrets) edit(fn func(keptKey, *keptSecret) bool) bool { return k.editGen(true, fn) }

// editGen is edit; revoking says the edit may revoke what a resolution under way would bring (a provider gone): it
// moves the generation, so that value is not kept. An edit that is not revoking moves it only when it drops one: a
// provider's restart or new configuration must not turn away a value resolved meanwhile.
func (k *keptSecrets) editGen(revoking bool, fn func(keptKey, *keptSecret) bool) bool {
	k.mu.Lock()
	defer k.mu.Unlock()
	changed, dropped := false, false
	for key, v := range k.kept {
		was := v
		if !fn(key, &v) {
			delete(k.kept, key)
			changed, dropped = true, true
		} else if v != was {
			k.kept[key] = v
			changed = true
		}
	}
	if revoking || dropped {
		k.gen++
	}
	return changed
}

// forget drops the kept values that match; whether there were any.
func (k *keptSecrets) forget(match func(keptKey, keptSecret) bool) bool {
	return k.edit(func(key keptKey, v *keptSecret) bool { return !match(key, *v) })
}

// forgetReference: the provider said reference is gone or refused it, so no kept copy of it is trusted any more.
func (k *keptSecrets) forgetReference(reference string) bool {
	return k.forget(func(key keptKey, _ keptSecret) bool { return key.reference == reference })
}

// staleProvider: provider may give other values now (it stored a secret).
func (k *keptSecrets) staleProvider(provider string) bool {
	return k.edit(func(_ keptKey, v *keptSecret) bool {
		if v.provider == provider {
			v.stale = true
		}
		return true
	})
}

// revokes says whether a failed resolution revokes what is kept of the reference: the provider (or the lack of one)
// says it is gone or refused, not that it cannot answer now.
func revokes(code string) bool {
	switch code {
	case "not_found", "refused", "no_provider", "not_reference":
		return true
	}
	return false // not_signed_in, provider_off, rate_limited, failed (a timeout too), too_long: may pass
}

// loadKept reads the sealed copies once; a read that fails is tried again at the next use. One that does not open
// (another key, a corrupt row) is left out, and goes at the next write.
func (h *Manager) loadKept(ctx context.Context) {
	k := &h.extensions.kept
	k.mu.Lock()
	loaded := k.loaded
	k.mu.Unlock()
	if loaded {
		return
	}
	var list []storedKeptSecret
	if h.store != nil {
		if _, err := h.store.Setting(ctx, launcherKeptKey, &list); err != nil {
			h.logf("launcher keys: the kept copies were not read: %v", err)
			return
		}
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.loaded {
		return
	}
	k.loaded = true
	if k.kept == nil {
		k.kept = map[keptKey]keptSecret{}
	}
	for _, e := range list {
		raw, err := h.openKey(e.ValueSealed)
		if err != nil {
			continue
		}
		var p keptPlain
		if json.Unmarshal([]byte(raw), &p) != nil || p.Reference == "" || p.Value == "" {
			continue
		}
		key := keptKey{reference: p.Reference, kind: "launcher", id: e.Launcher, machine: e.Machine}
		if e.Extension != "" {
			key = keptKey{reference: p.Reference, kind: "extension", id: e.Extension}
		}
		if key.id == "" || e.Launcher != "" && e.Extension != "" {
			continue
		}
		if _, ok := k.kept[key]; !ok {
			k.kept[key] = keptSecret{value: p.Value, provider: e.Provider, at: e.At, stale: e.Stale}
		}
	}
}

// saveKept writes the kept values, sealed, as they are now. Never a value in the log: the store's error is about the
// write, and what it was given is sealed.
func (h *Manager) saveKept(ctx context.Context) {
	if h.store == nil || h.extensions == nil {
		return
	}
	k := &h.extensions.kept
	k.saveMu.Lock()
	defer k.saveMu.Unlock()
	k.mu.Lock()
	if !k.loaded {
		k.mu.Unlock()
		return // what the database has was never read: writing now would lose it
	}
	type entry struct {
		key keptKey
		v   keptSecret
	}
	snap := make([]entry, 0, len(k.kept))
	for key, v := range k.kept {
		snap = append(snap, entry{key, v})
	}
	k.mu.Unlock()
	slices.SortFunc(snap, func(a, b entry) int {
		return cmp.Or(strings.Compare(a.key.kind, b.key.kind), strings.Compare(a.key.id, b.key.id),
			strings.Compare(a.key.machine, b.key.machine), strings.Compare(a.key.reference, b.key.reference))
	})
	list := make([]storedKeptSecret, 0, len(snap))
	for _, e := range snap {
		raw, _ := json.Marshal(keptPlain{Reference: e.key.reference, Value: e.v.value})
		sealed, err := h.sealKey(string(raw))
		if err != nil {
			h.logf("launcher keys: the kept copies were not sealed")
			return
		}
		st := storedKeptSecret{Launcher: e.key.id, Machine: e.key.machine, Provider: e.v.provider, At: e.v.at,
			Stale: e.v.stale, ValueSealed: sealed}
		if e.key.kind == "extension" {
			st.Launcher, st.Machine, st.Extension = "", "", e.key.id
		}
		list = append(list, st)
	}
	wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if err := h.store.PutSetting(wctx, launcherKeptKey, list); err != nil {
		h.logf("launcher keys: the kept copies were not saved: %v", err)
	}
}

// LauncherSecret is launcher's key for machine ("" for the manager's own calls) when the key is reference: what is
// kept of it, else what its provider gives now, kept from then on. Saving the reference in Settings approved it
// (approved by "settings"). Errors are *SecretError.
func (h *Manager) LauncherSecret(ctx context.Context, reference, launcher, machine string) (string, error) {
	if h.extensions == nil {
		return h.ResolveSecret(ctx, reference, wire.SecretPurpose{}, "settings") // says no provider is on
	}
	return h.keptSecret(ctx, keptKey{reference: reference, kind: "launcher", id: launcher, machine: machine})
}

// keptSecret is key's value: what is kept of it, else what its provider gives now (approved by "settings"), kept from
// then on. A stale one is asked for quietly (the provider must not ask the user) and answers while that fails.
func (h *Manager) keptSecret(ctx context.Context, key keptKey) (string, error) {
	h.loadKept(ctx)
	kept, stale, gen, ok := h.extensions.kept.get(key)
	if ok && !stale {
		return kept, nil
	}
	purpose := wire.SecretPurpose{Kind: key.kind, ID: key.id, Machine: key.machine}
	v, err := h.resolveSecret(ctx, key.reference, purpose, "settings", ok)
	if err != nil {
		var se *SecretError
		if ok && errors.As(err, &se) && !revokes(se.Code) {
			return kept, nil // stale, and the provider cannot say otherwise now
		}
		return "", err
	}
	h.keepSecret(ctx, key, v, gen)
	return v, nil
}

// RefreshLauncherSecret asks reference's provider for launcher's key on machine again, whatever is kept (the secret
// was rotated), and keeps what it gives. The provider's not_found or refused drops what was kept of the reference;
// any other failure leaves it, and is returned. Errors are *SecretError.
func (h *Manager) RefreshLauncherSecret(ctx context.Context, reference, launcher, machine string) error {
	if h.extensions == nil {
		_, err := h.ResolveSecret(ctx, reference, wire.SecretPurpose{}, "settings")
		return err
	}
	h.loadKept(ctx)
	key := keptKey{reference: reference, kind: "launcher", id: launcher, machine: machine}
	_, _, gen, _ := h.extensions.kept.get(key)
	v, err := h.ResolveSecret(ctx, reference, wire.SecretPurpose{Kind: "launcher", ID: launcher, Machine: machine}, "settings")
	if err != nil {
		return err
	}
	h.keepSecret(ctx, key, v, gen)
	return nil
}

func (h *Manager) keepSecret(ctx context.Context, key keptKey, v string, gen uint64) {
	prov, ok := h.SecretProviderOf(key.reference)
	if ok && h.extensions.kept.put(key, v, prov.Extension, gen) {
		h.saveKept(ctx)
	}
}

// ForgetLauncherSecrets drops launcher's kept keys: it is gone.
func (h *Manager) ForgetLauncherSecrets(launcher string) {
	h.RetainLauncherSecrets(context.Background(), func(l, _, _ string) bool { return l != launcher })
}

// RetainLauncherSecrets keeps the launcher keys keep says are still the launchers' (launcher, machine, reference) and
// drops the others: a launcher removed, a key that is another reference now, or no reference.
func (h *Manager) RetainLauncherSecrets(ctx context.Context, keep func(launcher, machine, reference string) bool) {
	if h.extensions == nil {
		return
	}
	h.loadKept(ctx)
	if h.extensions.kept.forget(func(key keptKey, _ keptSecret) bool {
		return key.kind == "launcher" && !keep(key.id, key.machine, key.reference)
	}) {
		h.saveKept(ctx)
	}
}

// ExtensionSecret is extension id's secret when it is reference: kept as a launcher key is (keptSecret).
func (ps *extensions) extensionSecret(ctx context.Context, id, reference string) (string, error) {
	if ps.h == nil {
		return ps.resolveSecret(ctx, reference, wire.SecretPurpose{Kind: "extension", ID: id}, "settings", false)
	}
	return ps.h.keptSecret(ctx, keptKey{reference: reference, kind: "extension", id: id})
}

// retainExtensionSecrets drops what is kept of extension id's secrets that are no longer one of references (its
// settings name another reference now, or a value).
func (ps *extensions) retainExtensionSecrets(ctx context.Context, id string, references map[string]string) {
	if ps.h == nil {
		return
	}
	ps.h.loadKept(ctx)
	// Not revoking: every start of every extension comes here, and one that drops nothing must not turn away a value
	// resolved meanwhile (a launcher's, another extension's).
	if ps.kept.editGen(false, func(key keptKey, _ *keptSecret) bool {
		if key.kind != "extension" || key.id != id {
			return true
		}
		for _, ref := range references {
			if ref == key.reference {
				return true
			}
		}
		return false
	}) {
		ps.h.saveKept(ctx)
	}
}

// forgetReference: reference's provider said it is gone or refused it.
func (ps *extensions) forgetReference(ctx context.Context, reference string) {
	if ps.h == nil {
		ps.kept.forgetReference(reference)
		return
	}
	ps.h.loadKept(ctx)
	if ps.kept.forgetReference(reference) {
		ps.h.saveKept(ctx)
	}
}

// keptProviderChanged is a registry change of provider id (old: its id before, when it was renamed) as the kept
// values take it: removed or switched off, or no longer claiming a kept reference's scheme, drops them. An upgrade, a
// restart, a new configuration that still claims the scheme keeps them: the values are the same secret's. An
// extension removed (or renamed) also drops the values kept of its own secrets.
func (ps *extensions) keptProviderChanged(ctx context.Context, c extension.Change) {
	old := ""
	if c.Old != nil && c.Old.Record.ID != c.ID {
		old = c.Old.Record.ID
	}
	var p *extension.Extension
	if c.Kind != "removed" {
		p = ps.reg.Snapshot(c.ID)
	}
	revoked := func(reference string) bool {
		switch {
		case p == nil || !p.Record.Enabled:
			return true // removed or switched off
		case p.Manifest == nil:
			return false // its manifest is not read now (a problem that may pass): nothing is known to have changed
		}
		scheme, ok := extension.ReferenceScheme(reference)
		if !ok {
			return true
		}
		return !slices.Contains(p.Manifest.Schemes(), scheme)
	}
	if ps.h != nil {
		ps.h.loadKept(ctx)
	}
	// Removed, switched off or renamed may revoke a value resolved meanwhile; a restart, an upgrade that keeps the
	// schemes or a new configuration does not (an upgrade that drops a scheme drops what it kept of it).
	revoking := c.Kind == "removed" || old != "" || p == nil || !p.Record.Enabled
	changed := ps.kept.editGen(revoking, func(key keptKey, v *keptSecret) bool {
		if key.kind == "extension" && (key.id == old || c.Kind == "removed" && key.id == c.ID) {
			return false // the extension that held the reference is gone (or another now: it resolves again)
		}
		if v.provider != c.ID && (old == "" || v.provider != old) {
			return true
		}
		if revoked(key.reference) {
			return false
		}
		v.provider = c.ID // renamed: the same provider
		return true
	})
	if changed && ps.h != nil {
		ps.h.saveKept(ctx)
	}
}

// keptProviderStored: provider stored a secret (secrets.store): a kept value of it may be another now.
func (ps *extensions) keptProviderStored(ctx context.Context, provider string) {
	if ps.h != nil {
		ps.h.loadKept(ctx)
	}
	if ps.kept.staleProvider(provider) && ps.h != nil {
		ps.h.saveKept(ctx)
	}
}
