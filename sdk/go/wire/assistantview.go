package wire

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"
)

const (
	AssistantViewMost  = 64 << 10
	AssistantPagesMost = 8
)

// assistantPageName is compiled on first use: 64 copies of two Unicode classes take about 1 ms and 2.5 MB, paid by
// every program that links this package when it starts (a hook, #1308).
var assistantPageName = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`^[\p{L}\p{N}_-]{1,64}$`) })

func ParseAssistantView(raw json.RawMessage) (*View, error) {
	if len(raw) > AssistantViewMost {
		return nil, fmt.Errorf("view: at most %d bytes", AssistantViewMost)
	}
	if err := checkAssistantJSONDepth(raw); err != nil {
		return nil, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, fmt.Errorf("view: %w", err)
	}
	if fields == nil {
		return nil, errors.New("view: an object with sections and fallback is required")
	}
	if _, exists := fields["fallback"]; !exists {
		return nil, errors.New("fallback: include a plain text summary under the exact fallback field name")
	}
	if _, component := fields["type"]; component {
		fallback := fields["fallback"]
		delete(fields, "fallback")
		item, err := json.Marshal(fields)
		if err != nil {
			return nil, err
		}
		if err := ValidateJSON("Component", item); err != nil {
			return nil, err
		}
		if len(fallback) == 0 {
			return nil, errors.New("fallback: include a plain text summary of the card")
		}
		raw = append([]byte(`{"fallback":`), fallback...)
		raw = append(raw, []byte(`,"sections":[{"items":[`)...)
		raw = append(raw, item...)
		raw = append(raw, []byte(`]}]}`)...)
	}
	var v View
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, err
	}
	if len(v.Pages) > AssistantPagesMost {
		return nil, fmt.Errorf("pages: at most %d named pages", AssistantPagesMost)
	}
	for name, page := range v.Pages {
		if len(page.Pages) > 0 {
			return nil, fmt.Errorf("pages.%s: detail pages cannot nest pages", name)
		}
	}
	if err := ValidateJSON("View", raw); err != nil {
		return nil, err
	}
	return &v, CheckAssistantView(&v)
}

func checkAssistantJSONDepth(raw []byte) error {
	depth := 0
	quoted, escaped := false, false
	for _, b := range raw {
		if quoted {
			switch {
			case escaped:
				escaped = false
			case b == '\\':
				escaped = true
			case b == '"':
				quoted = false
			}
			continue
		}
		switch b {
		case '"':
			quoted = true
		case '{', '[':
			if depth++; depth > 32 {
				return errors.New("view: JSON nesting exceeds 32 levels")
			}
		case '}', ']':
			depth--
		}
	}
	return nil
}

func CheckAssistantView(v *View) error {
	if v == nil {
		return errors.New("view: an object with sections and fallback is required")
	}
	if strings.TrimSpace(v.Fallback) == "" {
		return errors.New("fallback: include a plain text summary of the card")
	}
	if len(v.Pages) > AssistantPagesMost {
		return fmt.Errorf("pages: at most %d named pages", AssistantPagesMost)
	}
	for name, page := range v.Pages {
		if !assistantPageName().MatchString(name) {
			return fmt.Errorf("pages.%s: use 1–64 letters, numbers, underscores or hyphens", name)
		}
		if len(page.Pages) > 0 {
			return fmt.Errorf("pages.%s: detail pages refer to the root pages; do not nest pages", name)
		}
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("view: %w", err)
	}
	if len(raw) > AssistantViewMost {
		return fmt.Errorf("view: at most %d bytes", AssistantViewMost)
	}
	ids := map[string]bool{}
	checkPage := func(page *View, where string) error {
		if err := CheckView(page); err != nil {
			return fmt.Errorf("%s: %w", where, err)
		}
		for i, s := range page.Sections {
			for j, e := range s.Items {
				if e.Type == "field" && e.Kind == "secret" {
					return fmt.Errorf("%s.sections[%d].items[%d]: assistant cards must not collect secrets", where, i, j)
				}
				// UI version 6: no extension serves an assistant card's pictures, and a control acts at once, which a
				// card's actions (a reply the user confirms) do not.
				if HasImageRef(&e) {
					return fmt.Errorf("%s.sections[%d].items[%d]: assistant cards show pictures by https url, not ref", where, i, j)
				}
				if e.Control != nil || slices.ContainsFunc(e.Tiles, func(t Tile) bool { return t.Control != nil }) {
					return fmt.Errorf("%s.sections[%d].items[%d]: assistant cards have no controls; use a button", where, i, j)
				}
			}
		}
		for _, a := range Actions(page) {
			if ids[a.ID] {
				return fmt.Errorf("%s: action %q is repeated; use a unique id across the card's pages", where, a.ID)
			}
			ids[a.ID] = true
			if err := checkAssistantAction(a, v.Pages); err != nil {
				return fmt.Errorf("%s.action.%s: %w", where, a.ID, err)
			}
		}
		return nil
	}
	if err := checkPage(v, "view"); err != nil {
		return err
	}
	for _, name := range sortedPageNames(v.Pages) {
		page := v.Pages[name]
		if err := checkPage(&page, "pages."+name); err != nil {
			return err
		}
	}
	return nil
}

func sortedPageNames(pages map[string]View) []string {
	names := make([]string, 0, len(pages))
	for name := range pages {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func checkAssistantAction(a *Action, pages map[string]View) error {
	n := 0
	for _, yes := range []bool{a.Reply != "", a.Open != "", a.Push != nil, a.Pop} {
		if yes {
			n++
		}
	}
	if n != 1 {
		return errors.New("choose exactly one of reply, open, push or pop")
	}
	if a.Submit && a.Reply == "" {
		return errors.New("submit requires reply")
	}
	if a.Reply != "" && strings.TrimSpace(a.Reply) == "" {
		return errors.New("reply must contain text")
	}
	if a.Open != "" {
		return CheckAssistantOpen(a.Open)
	}
	if p := a.Push; p != nil {
		if p.Page == "" || p.UI != "" || p.State != "" {
			return errors.New("push requires page, without extension ui or state")
		}
		if _, exists := pages[p.Page]; !exists {
			return fmt.Errorf("push.page %q is missing from the root pages", p.Page)
		}
	}
	return nil
}

func parseAssistantOpen(target string) (*url.URL, error) {
	if strings.IndexFunc(target, unicode.IsControl) >= 0 || strings.Contains(target, "\\") {
		return nil, errors.New("open: no control characters or backslashes")
	}
	u, err := url.Parse(target)
	if err != nil || u.User != nil || u.Hostname() == "" {
		return nil, errors.New("open: a URL without credentials is required")
	}
	if u.Scheme == "https" {
		return u, nil
	}
	if !assistantURLScheme(u.Scheme) || u.ForceQuery || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" || strings.ContainsAny(target, "?#") {
		return nil, errors.New("open: use https or a persona session, task, kb or file link")
	}
	rawPath := ""
	if rest, ok := strings.CutPrefix(target[len(u.Scheme):], "://"); ok {
		if slash := strings.IndexByte(rest, '/'); slash >= 0 {
			rawPath = rest[slash:]
		}
	}
	if !strings.HasPrefix(rawPath, "/") {
		return nil, errors.New("open: invalid path segment")
	}
	parts := strings.Split(strings.TrimPrefix(rawPath, "/"), "/")
	decoded := make([]string, len(parts))
	for i, part := range parts {
		value, err := url.PathUnescape(part)
		if err != nil || value == "" || !utf8.ValidString(value) || strings.IndexFunc(value, unicode.IsControl) >= 0 || strings.Contains(value, "\\") {
			return nil, errors.New("open: invalid path segment")
		}
		if value == "." || value == ".." {
			return nil, errors.New("open: paths cannot traverse directories")
		}
		if u.Host != "file" && strings.Contains(value, "/") {
			return nil, errors.New("open: do not escape multiple path segments into one")
		}
		decoded[i] = value
	}
	switch u.Host {
	case "session":
		if len(parts) == 2 {
			return u, nil
		}
	case "task":
		if len(parts) == 1 {
			return u, nil
		}
	case "kb":
		return u, nil
	case "file":
		if len(parts) == 2 && !strings.Contains(decoded[0], "/") && path.IsAbs(decoded[1]) {
			return u, nil
		}
	}
	return nil, errors.New("open: the target has the wrong kind or number of path segments")
}

func CheckAssistantOpen(target string) error {
	_, err := parseAssistantOpen(target)
	return err
}

// CheckExtensionOpen accepts the two internal destinations a full extension page may open: a Persona session
// (persona://session/<machine>/<session>) and a to-do (persona://task/<id>). It applies the same structural checks as
// CheckAssistantOpen; every other kind stays assistant-only.
func CheckExtensionOpen(target string) error {
	u, err := parseAssistantOpen(target)
	if err != nil {
		return err
	}
	if !assistantURLScheme(u.Scheme) {
		return errors.New("open: an extension page may open only a persona session or task")
	}
	switch u.Host {
	case "session", "task":
		return nil
	}
	return errors.New("open: an extension page may open only a persona session or task")
}

func AssistantOnlyAction(a *Action) bool {
	return assistantOnlyAction(a, false)
}

func assistantOnlyAction(a *Action, keepPageNavigation bool) bool {
	if a == nil {
		return false
	}
	if a.Reply != "" || a.Pop || a.Push != nil && a.Push.Page != "" {
		return true
	}
	if a.Open == "" {
		return false
	}
	// A full extension page may navigate to the two internal destinations below (review of #14617): its own
	// rendering stays declarative, and a tap only routes. Every other assistant-only open target stays stripped.
	return !(keepPageNavigation && CheckExtensionOpen(a.Open) == nil)
}

func StripAssistantFields(v *View) []string { return stripAssistantFields(v, false) }

// StripAssistantFieldsForPage is StripAssistantFields for a full extension page (extension_page/plugin_page and
// nav_page): it keeps an action whose only assistant-only field is an open of a persona session or to-do.
func StripAssistantFieldsForPage(v *View) []string { return stripAssistantFields(v, true) }

func stripAssistantFields(v *View, keepPageNavigation bool) []string {
	var dropped []string
	drop := func(name string) {
		for _, old := range dropped {
			if old == name {
				return
			}
		}
		dropped = append(dropped, name)
	}
	if v == nil {
		return dropped
	}
	if v.Fallback != "" {
		drop("fallback")
		v.Fallback = ""
	}
	if len(v.Pages) > 0 {
		drop("pages")
		v.Pages = nil
	}
	if v.Search != nil && assistantOnlyAction(&v.Search.Action, keepPageNavigation) {
		drop("assistant_action")
		v.Search = nil
	}
	if v.Filter != nil && assistantOnlyAction(&v.Filter.Action, keepPageNavigation) {
		drop("assistant_action")
		v.Filter = nil
	}
	filterRowActions := func(in []RowAction) []RowAction {
		var out []RowAction
		changed := false
		for i := range in {
			if assistantOnlyAction(&in[i].Action, keepPageNavigation) {
				drop("assistant_action")
				if !changed {
					out = append([]RowAction(nil), in[:i]...)
					changed = true
				}
				continue
			}
			if changed {
				out = append(out, in[i])
			}
		}
		if !changed {
			return in
		}
		return out
	}
	filterComposerButtons := func(in []ComposerButton) ([]ComposerButton, bool) {
		var out []ComposerButton
		changed := false
		for i := range in {
			if assistantOnlyAction(&in[i].Action, keepPageNavigation) {
				drop("assistant_action")
				if !changed {
					out = append([]ComposerButton(nil), in[:i]...)
					changed = true
				}
				continue
			}
			if changed {
				out = append(out, in[i])
			}
		}
		if !changed {
			return in, false
		}
		return out, true
	}
	if v.Tabs != nil && assistantOnlyAction(&v.Tabs.Action, keepPageNavigation) {
		drop("assistant_action")
		v.Tabs = nil
	}
	if v.Review != nil {
		if kept, changed := filterComposerButtons(v.Review.Buttons); changed {
			if len(kept) == 0 {
				v.Review = nil
			} else {
				v.Review.Buttons = kept
			}
		}
		if v.Review != nil && assistantOnlyAction(v.Review.Discard, keepPageNavigation) {
			drop("assistant_action")
			v.Review.Discard = nil
		}
	}
	sections := make([]Section, 0, len(v.Sections))
	for i := range v.Sections {
		s := &v.Sections[i]
		hadItems := len(s.Items) > 0
		if s.More != nil && assistantOnlyAction(&s.More.Action, keepPageNavigation) {
			drop("assistant_action")
			s.More = nil
		}
		if s.Link != nil && assistantOnlyAction(&s.Link.Action, keepPageNavigation) {
			drop("assistant_action")
			s.Link = nil
		}
		items := make([]Component, 0, len(s.Items))
		for _, e := range s.Items {
			if e.Type == "html" {
				drop("html")
				continue
			}
			if assistantOnlyAction(e.Action, keepPageNavigation) {
				drop("assistant_action")
				if e.Type == "button" || e.Type == "segmented" {
					continue
				}
				e.Action = nil
			}
			if e.Button != nil && assistantOnlyAction(&e.Button.Action, keepPageNavigation) {
				drop("assistant_action")
				e.Button = nil
			}
			if kept, changed := filterComposerButtons(e.Buttons); changed {
				if e.Type == ComponentComposer && len(kept) == 0 {
					continue
				}
				e.Buttons = kept
			}
			for _, ap := range []**Action{&e.Expand, &e.Load, &e.Comment, &e.Reply, &e.Context, &e.MarkViewed, &e.Draft, &e.Navigate} {
				if assistantOnlyAction(*ap, keepPageNavigation) {
					drop("assistant_action")
					*ap = nil
				}
			}
			if e.Control != nil && assistantOnlyAction(&e.Control.Action, keepPageNavigation) {
				drop("assistant_action")
				e.Control = nil
			}
			e.Actions = filterRowActions(e.Actions)
			for k := range e.Crumbs {
				if assistantOnlyAction(e.Crumbs[k].Action, keepPageNavigation) {
					drop("assistant_action")
					e.Crumbs[k].Action = nil
				}
			}
			for k := range e.Tiles {
				t := &e.Tiles[k]
				if assistantOnlyAction(t.Action, keepPageNavigation) {
					drop("assistant_action")
					t.Action = nil
				}
				if t.Control != nil && assistantOnlyAction(&t.Control.Action, keepPageNavigation) {
					drop("assistant_action")
					t.Control = nil
				}
				t.Actions = filterRowActions(t.Actions)
			}
			// a graph's nodes keep their place; only their assistant-only action goes
			for k := range e.Nodes {
				if assistantOnlyAction(e.Nodes[k].Action, keepPageNavigation) {
					drop("assistant_action")
					e.Nodes[k].Action = nil
				}
			}
			// and so do a scene's pins
			for k := range e.Pins {
				if assistantOnlyAction(e.Pins[k].Action, keepPageNavigation) {
					drop("assistant_action")
					e.Pins[k].Action = nil
				}
			}
			// and a map's markers
			for k := range e.Markers {
				if assistantOnlyAction(e.Markers[k].Action, keepPageNavigation) {
					drop("assistant_action")
					e.Markers[k].Action = nil
				}
			}
			for k := range e.Threads {
				if assistantOnlyAction(e.Threads[k].Reply, keepPageNavigation) {
					drop("assistant_action")
					e.Threads[k].Reply = nil
				}
				if assistantOnlyAction(e.Threads[k].Resolve, keepPageNavigation) {
					drop("assistant_action")
					e.Threads[k].Resolve = nil
				}
			}
			items = append(items, e)
		}
		s.Items = items
		if !hadItems || len(items) > 0 || s.Footer != "" || s.More != nil {
			sections = append(sections, *s)
		}
	}
	v.Sections = sections
	return dropped
}
