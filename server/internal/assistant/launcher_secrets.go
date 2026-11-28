package assistant

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/extmgr"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

// Launcher keys that are references (docs/extensions/secret-providers.md "Launcher keys", #3962): a model provider's
// key, or one computer's key for it, may be a reference such as op://vault/item/field in place of the key. It is
// sealed in the same field behind the extensions' marker (extension.SealedReference), so nothing else about a stored
// provider changes, and the provider extension that claims its scheme (internal/extmgr, in this process) gives the
// value wherever the key goes out: a computer's launchers.pull, the manager's own calls (main chat, quota, probe).
// Saving the reference in Settings is the owner's standing approval for that launcher; the manager keeps the value its
// provider gave, in memory and sealed in the database (extmgr secret_kept.go), until it is revoked: the launcher's key
// is no longer that reference (retainLauncherSecrets, at every store of the launchers), the provider goes, or it says
// the reference is gone. POST /api/v1/launchers/{name}/refresh-key asks the provider again (the secret was rotated).

// launcherSecrets is what the launchers ask of the provider extensions (extmgr.Manager).
type launcherSecrets interface {
	// LauncherSecret is reference's value for launcher on machine ("" for the manager's own calls).
	LauncherSecret(ctx context.Context, reference, launcher, machine string) (string, error)
	// RefreshLauncherSecret asks reference's provider again for launcher on machine and keeps what it gives.
	RefreshLauncherSecret(ctx context.Context, reference, launcher, machine string) error
	// ForgetLauncherSecrets drops what is kept of launcher's keys.
	ForgetLauncherSecrets(launcher string)
	// RetainLauncherSecrets drops what is kept of the launchers' keys that keep says are not theirs any more.
	RetainLauncherSecrets(ctx context.Context, keep func(launcher, machine, reference string) bool)
	SecretProviderOf(reference string) (extension.SecretProvider, bool)
	// SecretReference is reference as a settings field shows it.
	SecretReference(ctx context.Context, reference string) *extmgr.SecretReferenceView
}

var _ launcherSecrets = (*extmgr.Manager)(nil)

// keyReference is the reference a sealed key holds; false for a key, no key or one that does not open.
func (s *Service) keyReference(sealed string) (string, bool) {
	if sealed == "" {
		return "", false
	}
	v, err := s.openKey(sealed)
	if err != nil {
		return "", false
	}
	return extension.ReferenceOf(v)
}

// openLauncherKey is a sealed key of launcher as it is used on machine ("" for the manager's own calls): the key, or
// the value its reference resolves to. A reference that does not resolve is the provider's error (*extmgr.SecretError,
// in the asker's language), so a start it refuses says why.
func (s *Service) openLauncherKey(ctx context.Context, sealed, launcher, machine string) (string, error) {
	if sealed == "" {
		return "", nil
	}
	v, err := s.openKey(sealed)
	if err != nil {
		return "", i18n.Err("assistant-models.err_key_decrypt")
	}
	ref, ok := extension.ReferenceOf(v)
	if !ok {
		return v, nil
	}
	if s.refs == nil {
		return "", noSecretProvider(ref)
	}
	return s.refs.LauncherSecret(ctx, ref, launcher, machine)
}

func noSecretProvider(ref string) error {
	scheme, _ := extension.ReferenceScheme(ref)
	return i18n.Err("hub-extension_secrets.no_provider", i18n.Args{"scheme": scheme + "://"})
}

// sealKeyReference checks a reference for launcher's key (machine's own when set) and seals it: one of a scheme an
// enabled provider extension claims, that resolves. A reference the launcher's key already is answers from what is
// kept of it (nobody is asked: the provider may be locked); a new one is asked for now. Called before models.mu is
// taken: it may ask the provider.
func (s *Service) sealKeyReference(ctx context.Context, ref, launcher, machine string) (string, error) {
	ref = strings.TrimSpace(ref)
	if _, ok := extension.ReferenceScheme(ref); !ok {
		return "", i18n.Err("hub-extension_secrets.not_reference")
	}
	if s.refs == nil {
		return "", noSecretProvider(ref)
	}
	if _, ok := s.refs.SecretProviderOf(ref); !ok {
		return "", noSecretProvider(ref)
	}
	if _, err := s.refs.LauncherSecret(ctx, ref, launcher, machine); err != nil {
		return "", err
	}
	return s.sealKey(extension.SealedReference(ref))
}

// sealKeyReferences seals in's references (api_key_reference, machine_key_references) into it. Saving one again (or
// changing anything else: the scope, the model, the proxy) keeps what is kept of it; a key that changes is dropped
// once the launchers are stored (retainLauncherSecrets), and refresh-key asks for a rotated secret.
func (s *Service) sealKeyReferences(ctx context.Context, in *launcherInput) error {
	if in.APIKeyReference != nil && strings.TrimSpace(*in.APIKeyReference) != "" {
		sealed, err := s.sealKeyReference(ctx, *in.APIKeyReference, in.Name, "")
		if err != nil {
			return err
		}
		in.keyRefSealed = sealed
	}
	for machine, ref := range in.MachineKeyReferences {
		if ref == nil || strings.TrimSpace(*ref) == "" {
			continue
		}
		sealed, err := s.sealKeyReference(ctx, *ref, in.Name, machine)
		if err != nil {
			return err
		}
		if in.machineRefsSealed == nil {
			in.machineRefsSealed = map[string]string{}
		}
		in.machineRefsSealed[machine] = sealed
	}
	return nil
}

func (s *Service) forgetLauncherSecrets(launcher string) {
	if s.refs != nil {
		s.refs.ForgetLauncherSecrets(launcher)
	}
}

// retainLauncherSecrets drops what is kept of the launchers' keys that are not theirs any more, by list (the launchers
// as stored, channels' aliases too) and keys (the computers' own keys): a launcher gone, a key that is another
// reference now or no reference. Called after the launchers were stored.
func (s *Service) retainLauncherSecrets(ctx context.Context, list []storedLauncher, keys []machineKey) {
	if s.refs == nil {
		return
	}
	s.refs.RetainLauncherSecrets(ctx, func(launcher, machine, ref string) bool {
		i := slices.IndexFunc(list, func(l storedLauncher) bool { return l.Name == launcher })
		if i < 0 {
			return false
		}
		sealed := list[i].KeySealed
		if machine != "" {
			sealed = s.keySealedFor(list[i], keys, machine)
		}
		got, ok := s.keyReference(sealed)
		return ok && got == ref
	})
}

// launcherKeyUse is one of a launcher's keys that is a reference: for machine's own key, or the launcher's ("").
type launcherKeyUse struct {
	Machine, reference string
}

// launcherKeyUses are launcher's keys that are references: its own, then each computer's own.
func (s *Service) launcherKeyUses(l storedLauncher, keys []machineKey) []launcherKeyUse {
	var out []launcherKeyUse
	if ref, ok := s.keyReference(l.KeySealed); ok {
		out = append(out, launcherKeyUse{reference: ref})
	}
	for _, k := range keys {
		if k.Launcher != l.Name || k.Machine == "" {
			continue
		}
		if ref, ok := s.keyReference(k.KeySealed); ok {
			out = append(out, launcherKeyUse{Machine: k.Machine, reference: ref})
		}
	}
	return out
}

// withKeyReferences fills the managed views' key_reference and machine_key_references: where a key is a reference,
// the reference and what it names, never a value.
func (s *Service) withKeyReferences(ctx context.Context, views []LauncherView, keys []machineKey) {
	view := func(ref string) *extmgr.SecretReferenceView {
		if s.refs == nil {
			return &extmgr.SecretReferenceView{Reference: ref, Title: ref}
		}
		return s.refs.SecretReference(ctx, ref)
	}
	for i := range views {
		if views[i].Source != "persona" {
			continue
		}
		if ref, ok := s.keyReference(views[i].keySealed); ok {
			views[i].KeyReference = view(ref)
		}
		for _, k := range keys {
			if k.Launcher != views[i].Name {
				continue
			}
			if ref, ok := s.keyReference(k.KeySealed); ok {
				if views[i].MachineKeyReferences == nil {
					views[i].MachineKeyReferences = map[string]*extmgr.SecretReferenceView{}
				}
				views[i].MachineKeyReferences[k.Machine] = view(ref)
			}
		}
	}
}

// launcherReferences are the stored providers' keys that hold references, for the provider extensions' details.
func (s *Service) launcherReferences(ctx context.Context) []extmgr.LauncherReference {
	var out []extmgr.LauncherReference
	stored := s.storedLaunchers(ctx)
	name := func(launcher string) string {
		for _, l := range stored {
			if l.Name == launcher && l.DisplayName != "" {
				return l.DisplayName
			}
		}
		return launcher
	}
	for _, l := range stored {
		if ref, ok := s.keyReference(l.KeySealed); ok {
			out = append(out, extmgr.LauncherReference{Launcher: l.Name, LauncherName: name(l.Name), Reference: ref})
		}
	}
	var keys []machineKey
	if st := s.store(); st != nil {
		_, _ = memoSetting(ctx, st, launchersMachineKeysKey, &keys)
	}
	for _, k := range keys {
		if !slices.ContainsFunc(stored, func(l storedLauncher) bool { return l.Name == k.Launcher }) {
			continue
		}
		if ref, ok := s.keyReference(k.KeySealed); ok {
			out = append(out, extmgr.LauncherReference{Launcher: k.Launcher, LauncherName: name(k.Launcher), Machine: k.Machine, Reference: ref})
		}
	}
	return out
}

// refreshLauncherKey is POST /api/v1/launchers/{name}/refresh-key: each of the launcher's keys that is a reference is
// asked for again, its own first, then each computer's own. All answered: {launcher, refreshed: [{machine?}]}. The
// first that is not: launcher_key_unresolved with the provider's words and secret_code; kept says whether what was
// kept of it still answers (true for a failure that may pass: not signed in, the provider off, rate limited, a
// timeout; false when the provider said it is gone or refused it, which dropped it).
func (s *Service) refreshLauncherKey(w http.ResponseWriter, r *http.Request) {
	ctx, name := r.Context(), r.PathValue("name")
	list, err := s.readStoredLaunchers(ctx)
	if err != nil {
		apierr.Write(w, http.StatusInternalServerError, err)
		return
	}
	i := slices.IndexFunc(list, func(l storedLauncher) bool { return l.Name == name })
	if i < 0 {
		apierr.Write(w, http.StatusNotFound, i18n.Err("assistant-models_launchers.err_not_managed"))
		return
	}
	keys, err := s.readMachineKeys(ctx)
	if err != nil {
		apierr.Write(w, http.StatusInternalServerError, err)
		return
	}
	uses := s.launcherKeyUses(list[i], keys)
	if len(uses) == 0 {
		apierr.Write(w, http.StatusBadRequest, i18n.Err("hub-extension_secrets.not_reference"))
		return
	}
	if s.refs == nil {
		apierr.Write(w, http.StatusBadRequest, noSecretProvider(uses[0].reference))
		return
	}
	for _, u := range uses {
		if err := s.refs.RefreshLauncherSecret(ctx, u.reference, name, u.Machine); err != nil {
			status, code, kept := http.StatusServiceUnavailable, "failed", true
			var se *extmgr.SecretError
			if errors.As(err, &se) {
				code = se.Code
				switch se.Code {
				case "not_found", "refused", "no_provider", "not_reference":
					status, kept = http.StatusConflict, false
				}
			}
			body := apierr.CodeBody(w, status, proto.CodeLauncherKeyUnresolved, err)
			body["secret_code"], body["kept"] = code, kept
			if u.Machine != "" {
				body["machine"] = u.Machine
			}
			apierr.WriteBody(w, status, body)
			return
		}
	}
	s.invalidateLauncherListCache("")
	s.pushNodeLaunchers(ctx, "", list, keys) // the computers pull the new value
	refreshed := make([]map[string]string, 0, len(uses))
	for _, u := range uses {
		m := map[string]string{}
		if u.Machine != "" {
			m["machine"] = u.Machine
		}
		refreshed = append(refreshed, m)
	}
	apierr.WriteJSON(w, map[string]any{"launcher": s.withKeyMachines(ctx, []LauncherView{s.launcherView(list[i])})[0],
		"refreshed": refreshed})
}
