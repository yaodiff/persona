package extmgr

import (
	"context"
	"errors"
	"slices"
	"sort"
	"time"

	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/i18n"
)

// References in an extension's secrets (docs/extensions/secret-providers.md "References in an extension's secrets"):
// saving one in Settings is the owner's standing approval for that extension, so it is resolved, without a card, the
// first time the manager hands the extension its secrets, and the extension gets the value as if it had been typed in.
// The value is kept sealed from then on (secret_kept.go): a later start (a restart, an upgrade) asks the provider
// nothing, so it never has the user authorize it again.

// secretDescribeForView bounds asking a provider for a reference's words while a details page is drawn.
const secretDescribeForView = 2 * time.Second

// SecretReferenceView is a secret that holds a reference, as its extension's details show it: "From 1Password:
// op://…" with what it names under it.
type SecretReferenceView struct {
	Provider     string `json:"provider"`
	ProviderName string `json:"provider_name"`
	Reference    string `json:"reference"`
	// Title is what the reference names (the provider's words; the reference itself when it can't be asked).
	Title string `json:"title"`
}

// SecretUserView is a secret of another extension that holds one of a provider's references, as the provider's
// details list them: who its approvals cover.
type SecretUserView struct {
	// Kind is what holds it: "extension" (Extension's secret Secret) or "launcher" (model provider Launcher's key, the
	// one for computer Machine when that is set).
	Kind          string `json:"kind"`
	Extension     string `json:"extension,omitempty"`
	ExtensionName string `json:"extension_name,omitempty"`
	Secret        string `json:"secret,omitempty"`
	SecretTitle   string `json:"secret_title,omitempty"`
	EntryTitle    string `json:"entry_title,omitempty"`
	Launcher      string `json:"launcher,omitempty"`
	LauncherName  string `json:"launcher_name,omitempty"`
	Machine       string `json:"machine,omitempty"`
	Reference     string `json:"reference"`
}

// LauncherReference is a model provider's key that holds a reference (the assistant's launchers, Options.LauncherReferences).
type LauncherReference struct {
	Launcher, LauncherName string
	Machine                string // "": the provider's own key; else that computer's
	Reference              string
}

// programSecrets are extension id's secrets as its program gets them: values as stored, references resolved. One
// that does not resolve fails the whole hand-over, so a program never starts with a secret missing or a reference in
// place of its value.
func (ps *extensions) programSecrets(ctx context.Context, id string) (map[string]string, error) {
	stored, err := ps.reg.Secrets(ctx, id)
	if err != nil || stored == nil {
		return stored, err
	}
	out, err := ps.resolveStored(ctx, id, stored)
	if err == nil {
		_, refs := extension.SplitSecrets(stored)
		ps.retainExtensionSecrets(ctx, id, refs) // what it holds now: a reference it no longer names is not kept
	}
	return out, err
}

// resolveStored resolves the references among stored (extension id's secrets, or the ones a change would leave it).
func (ps *extensions) resolveStored(ctx context.Context, id string, stored map[string]string) (map[string]string, error) {
	values, refs := extension.SplitSecrets(stored)
	names := make([]string, 0, len(refs))
	for name := range refs {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		v, err := ps.extensionSecret(ctx, id, refs[name]) // kept once resolved (secret_kept.go)
		if err != nil {
			return nil, err
		}
		values[name] = v
	}
	return values, nil
}

// secretReferenceView is how a stored reference shows in its extension's details.
func (ps *extensions) secretReferenceView(ctx context.Context, ref string) *SecretReferenceView {
	v := &SecretReferenceView{Reference: ref, Title: ref}
	scheme, _ := extension.ReferenceScheme(ref)
	if prov, ok := ps.reg.SecretProviderFor(scheme); ok {
		v.Provider, v.ProviderName = prov.Extension, prov.Name
	}
	if v.Provider == "" {
		return v
	}
	cctx, cancel := context.WithTimeout(ctx, secretDescribeForView)
	defer cancel()
	if d, err := ps.describeSecret(cctx, ref); err == nil {
		v.Title = d.Title
		if d.Detail != "" {
			v.Title += " · " + d.Detail
		}
	}
	return v
}

// secretUsers are the secrets of the installed extensions that hold references to p's schemes.
func (ps *extensions) secretUsers(ctx context.Context, p *extension.Extension, l i18n.Lang) []SecretUserView {
	schemes := p.Full.Schemes()
	if len(schemes) == 0 {
		return nil
	}
	var out []SecretUserView
	for _, r := range ps.reg.ReferencesTo(ctx, schemes) {
		u := SecretUserView{Kind: "extension", Extension: r.Extension, ExtensionName: r.Extension, Secret: r.Name, SecretTitle: r.Name, Reference: r.Reference}
		if holder := ps.reg.Snapshot(r.Extension); holder != nil {
			u.ExtensionName = holder.L10n.Name(l, holder.Full.Name)
			if spec, ok := holder.SecretDeclaration(r.Name); ok {
				u.SecretTitle = holder.L10n.Secret(l, spec).Title
			}
			u.EntryTitle, _ = holder.EntrySecretTitle(r.Name, l)
		}
		out = append(out, u)
	}
	if ps.h != nil && ps.h.opts.LauncherReferences != nil {
		for _, r := range ps.h.opts.LauncherReferences(ctx) {
			if scheme, ok := extension.ReferenceScheme(r.Reference); ok && slices.Contains(schemes, scheme) {
				out = append(out, SecretUserView{Kind: "launcher", Launcher: r.Launcher, LauncherName: r.LauncherName, Machine: r.Machine, Reference: r.Reference})
			}
		}
	}
	return out
}

// SecretReference is reference as a settings field shows it: its provider and what it names (asked of the provider
// for at most secretDescribeForView, the reference itself when it does not say).
func (h *Manager) SecretReference(ctx context.Context, reference string) *SecretReferenceView {
	if h.extensions == nil {
		return &SecretReferenceView{Reference: reference, Title: reference}
	}
	return h.extensions.secretReferenceView(ctx, reference)
}

// secretRefError is a resolution failure as a settings check reports it (in l).
func secretRefError(err error, l i18n.Lang) string {
	var se *SecretError
	if errors.As(err, &se) {
		return se.Text(l)
	}
	return i18n.Localize(err, l)
}
