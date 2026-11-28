package assistant

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/url"
	"slices"
	"strings"

	"github.com/nwverse/persona/sdk/go/wire"
	"github.com/nwverse/persona/server/internal/db"
)

// pageFromCard copies the definition returned by one versioned read. The page
// has its own subsequent versions and actions, starting with its owner's Keep.
func (s *Service) pageFromCard(ctx context.Context, pages pageStore, views viewStore, caller pageCaller, raw json.RawMessage) (*wire.View, json.RawMessage, error) {
	var source struct {
		ID      string `json:"id"`
		Version int64  `json:"version"`
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&source); err != nil {
		return nil, nil, fmt.Errorf("from_view: provide the live card's id and displayed version: %w", err)
	}
	if !strings.HasPrefix(source.ID, "view_") || source.Version < 1 {
		return nil, nil, errors.New("from_view: provide the id from view_show and the positive version of the card to copy")
	}
	row, err := views.AssistantView(ctx, caller.session, source.ID)
	if errors.Is(err, db.ErrAssistantViewNotFound) || err == nil && (row == nil || !s.sameMachine(row.MachineID, caller.machine)) {
		return nil, nil, errors.New("not_found: no such live card in this conversation on this machine")
	}
	if err != nil {
		return nil, nil, err
	}
	if row.Version != source.Version {
		return nil, nil, errors.New("version_conflict: the source card changed; use view_get and review its current contents before drafting")
	}
	// Draft offers also receive view_ IDs. A settled offer can be entirely
	// read-only, so checking for reserved action IDs alone would miss it.
	if _, err := pages.AssistantPageByOffer(ctx, source.ID); err == nil {
		return nil, nil, errors.New("from_view: this card is a dashboard offer; use page get to read the dashboard")
	} else if !errors.Is(err, db.ErrAssistantPageNotFound) {
		return nil, nil, err
	}
	v, stored, err := parsePageView(row.View)
	if err != nil {
		return nil, nil, fmt.Errorf("from_view: %w", err)
	}
	// A chat card can navigate to local session, task and file URLs. A
	// dashboard's tap handler opens HTTPS links, including on its detail pages.
	if err := checkCopiedPageLinks(v, "view"); err != nil {
		return nil, nil, err
	}
	for _, name := range slices.Sorted(maps.Keys(v.Pages)) {
		page := v.Pages[name]
		if err := checkCopiedPageLinks(&page, "pages."+name); err != nil {
			return nil, nil, err
		}
	}
	return v, stored, nil
}

func checkCopiedPageLinks(page *wire.View, where string) error {
	for _, action := range wire.Actions(page) {
		if action.Open == "" {
			continue
		}
		u, err := url.Parse(action.Open)
		if err != nil || u.Scheme != "https" || u.Host == "" {
			return fmt.Errorf("from_view: %s.action.%s.open: dashboards open HTTPS links; provide an explicit page view for this card", where, action.ID)
		}
	}
	return nil
}
