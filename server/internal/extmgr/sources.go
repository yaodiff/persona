package extmgr

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/nwverse/persona/server/internal/buswire"
	"github.com/nwverse/persona/server/internal/extwire"
	"github.com/nwverse/persona/server/internal/mailwire"
	"github.com/nwverse/persona/server/internal/rpc"
)

// Moving a source from one manager to another (extwire.MethodSource*, docs/assistant/managers.md): the hub asks the old manager
// to export it, gives what came back to the new one to import, then tells the old one to release it. Mail accounts are
// the only kind so far. A bundle holds the source's secrets in plaintext: it is never logged, and no error quotes it.

const sourceKindMail = "mail"

// The source methods wait for the manager before decoding bundles or references.
func handleSourceRequest[P, R any](ctx context.Context, h *Manager, method buswire.Method[P, R], params json.RawMessage, invalid error, run func(P) (R, error)) (any, bool, error) {
	if !h.ready(ctx) {
		return nil, true, errAssistantOffline
	}
	return handleExtensionRequest(method, params, invalid, run)
}

func invalidSourceRef(method string) error {
	return invalidExtensionParams(method + " needs a source")
}

func (h *Manager) exportSource(r extwire.SourceRef) (extwire.SourceBundle, error) {
	id, err := h.mailSource(extwire.SourceExportMethod.Name, r.Source)
	if err != nil {
		return extwire.SourceBundle{}, err
	}
	data, err := h.mail.ExportAccount(id)
	if errors.Is(err, mailwire.ErrNoAccount) {
		return extwire.SourceBundle{}, noSource(r.Source)
	}
	if err != nil {
		return extwire.SourceBundle{}, err
	}
	return extwire.SourceBundle{Source: r.Source, Kind: sourceKindMail, Data: data}, nil
}

func (h *Manager) releaseSource(r extwire.SourceRef) (buswire.None, error) {
	id, err := h.mailSource(extwire.SourceReleaseMethod.Name, r.Source)
	if err != nil {
		return buswire.None{}, err
	}
	if err := h.mail.DeleteAccount(id); err != nil {
		return buswire.None{}, err
	}
	h.logf("source %s released", r.Source)
	return buswire.None{}, nil
}

// importSource takes a source another manager exported.
func (h *Manager) importSource(b extwire.SourceBundle) (extwire.SourceRef, error) {
	kind, _, _ := strings.Cut(b.Source, ":")
	if b.Kind != sourceKindMail || kind != sourceKindMail {
		return extwire.SourceRef{}, &rpc.Error{Code: extwire.CodeSourceKind, Message: "this kind of source cannot move: " + b.Kind}
	}
	if h.mail == nil {
		return extwire.SourceRef{}, &rpc.Error{Code: extwire.CodeNoSource, Message: "mail is not set up on this manager"}
	}
	a, err := h.mail.ImportAccount(b.Data)
	if errors.Is(err, mailwire.ErrAccountExists) {
		return extwire.SourceRef{}, &rpc.Error{Code: extwire.CodeSourceExists, Message: "this manager has " + b.Source + " already"}
	}
	if err != nil {
		return extwire.SourceRef{}, err
	}
	ref := extwire.SourceRef{Source: sourceKindMail + ":" + a.ID}
	h.logf("source %s imported", ref.Source)
	return ref, nil
}

// mailSource resolves a reference to an account on this manager.
func (h *Manager) mailSource(method, source string) (string, error) {
	if source == "" {
		return "", invalidSourceRef(method)
	}
	kind, id, _ := strings.Cut(source, ":")
	if kind != sourceKindMail {
		return "", &rpc.Error{Code: extwire.CodeSourceKind, Message: "this kind of source cannot move: " + kind}
	}
	if h.mail == nil || id == "" {
		return "", noSource(source)
	}
	accounts, err := h.mail.ListAccounts()
	if err != nil {
		return "", err
	}
	for _, a := range accounts {
		if a.ID == id {
			return id, nil
		}
	}
	return "", noSource(source)
}

func noSource(source string) error {
	return &rpc.Error{Code: extwire.CodeNoSource, Message: "no source " + source + " here"}
}
