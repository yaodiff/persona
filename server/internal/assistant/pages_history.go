package assistant

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/nwverse/persona/sdk/go/wire"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/i18n"
)

func pageHistoryButton(p *db.AssistantPageRow, l i18n.Lang) wire.Component {
	title := pageWord(l, "history")
	state := pageState{V: p.Version, Form: "history"}
	return wire.Component{Type: wire.ComponentButton, Key: "persona.history", Title: title,
		Action: &wire.Action{ID: "persona.history", Push: &wire.Push{UI: p.ID, State: state.encode(), Title: title}}}
}

// The history screens accept only their own navigation and restore actions. An action from a stored
// page (even one the current page still has) cannot run from a historical preview.
func (r *pageRequest) historyAction(ctx context.Context, p *db.AssistantPageRow, action, arg string) (extension.ActionResult, error) {
	switch action {
	case "":
		if r.state.Form == "version" {
			return r.versionPreview(ctx, p, r.state.Rev)
		}
		return r.versionHistory(ctx, p, "")
	case "persona.history":
		return r.versionHistory(ctx, p, "")
	case "persona.dashboard":
		r.state = pageState{}
		return r.pageResult(ctx, p, "", "")
	case "persona.version":
		n, err := strconv.ParseInt(arg, 10, 64)
		if err == nil && n > 0 && r.state.Form == "history" {
			return r.versionPreview(ctx, p, n)
		}
	case "persona.restore_version":
		if r.state.Form == "version" && r.state.Rev > 0 && r.state.Rev < r.state.V {
			return r.restoreVersion(ctx, p)
		}
	}
	return extension.ActionResult{}, pagesRefusal(http.StatusBadRequest, "bad_request", "bad_action")
}

func (r *pageRequest) historyView(v wire.View, toast, backAction, backLabel string) (extension.ActionResult, error) {
	// Clients before UI v3 follow an action in place instead of pushing a screen onto a navigation stack.
	if r.version < 3 {
		v.Sections = append(v.Sections, wire.Section{Items: []wire.Component{{
			Type: wire.ComponentButton, Key: "persona.back", Title: pageWord(r.l, backLabel), Action: &wire.Action{ID: backAction},
		}}})
	}
	if err := r.fit(&v); err != nil {
		return extension.ActionResult{}, err
	}
	return extension.ActionResult{View: &v, Toast: toast}, nil
}

func (r *pageRequest) versionHistory(ctx context.Context, p *db.AssistantPageRow, toast string) (extension.ActionResult, error) {
	versions, err := r.st.AssistantPageVersionSummaries(ctx, p.ID)
	if err != nil {
		return extension.ActionResult{}, err
	}
	// The history read may already include an update since the page lookup. Bind rows to the same head
	// their list actually shows; a newer write after this point requires a fresh review.
	head, title := p.Version, p.Title
	if len(versions) > 0 {
		head, title = versions[0].Version, versions[0].Title
	}
	rows := make([]wire.Component, 0, len(versions))
	for _, version := range versions {
		label := versionLabel(r.l, version.Version)
		state := pageState{Form: "version", V: head, Rev: version.Version}
		key := fmt.Sprintf("persona.version:%d", version.Version)
		row := wire.Component{Type: wire.ComponentRow, Key: key, Title: label, Subtitle: versionByline(version, r.l, r.loc),
			Action: &wire.Action{ID: key, Push: &wire.Push{UI: p.ID, State: state.encode(), Title: label}}}
		if version.Version == head {
			row.Value = pageWord(r.l, "version_current")
		}
		rows = append(rows, row)
	}
	v := wire.View{Title: pageWord(r.l, "history"), State: pageState{V: head, Form: "history"}.encode(),
		Sections: []wire.Section{{Key: "persona.history", Title: title, Items: rows}}}
	return r.historyView(v, toast, "persona.dashboard", "back_dashboard")
}

func versionLabel(l i18n.Lang, revision int64) string {
	return i18n.T(l, "assistant-pages.version", i18n.Args{"number": revision})
}

func versionByline(version db.AssistantPageVersionRow, l i18n.Lang, loc *time.Location) string {
	writer := pageWord(l, "version_persona")
	switch version.By.Kind {
	case db.AssistantPageByOwner:
		writer = pageWord(l, "version_owner")
	case db.AssistantPageByRestore:
		writer = pageWord(l, "version_restorer")
	case db.AssistantPageByRoutine:
		writer = pageWord(l, "version_routine")
	}
	return i18n.T(l, "assistant-pages.version_byline", i18n.Args{"writer": writer, "time": version.At.In(loc).Format("2006-01-02 15:04")})
}

func (r *pageRequest) versionPreview(ctx context.Context, p *db.AssistantPageRow, revision int64) (extension.ActionResult, error) {
	if r.state.V != p.Version {
		return r.versionHistory(ctx, p, pageWord(r.l, "version_changed"))
	}
	version, err := r.st.AssistantPageVersion(ctx, p.ID, revision)
	if errors.Is(err, db.ErrAssistantPageVersionNotFound) {
		return r.versionHistory(ctx, p, pageWord(r.l, "version_gone"))
	}
	if err != nil {
		return extension.ActionResult{}, err
	}
	items := []wire.Component{{Type: wire.ComponentText, Key: "persona.by", Style: "caption", Text: versionByline(*version, r.l, r.loc)}}
	// Each stored page has a plain-text fallback, except watch-only dashboards whose readings are live.
	// Construct a fresh View so links, actions, fields, and nested pages from old content stay inert.
	if !emptyPageView(version.View) {
		saved, err := wire.ParseAgentPage(version.View)
		if err != nil {
			return extension.ActionResult{}, err
		}
		items = append(items, wire.Component{Type: wire.ComponentText, Key: "persona.summary", Text: saved.Fallback})
		items = append(items, versionContents(saved)...)
	}
	if revision < p.Version {
		items = append(items, wire.Component{Type: wire.ComponentButton, Key: "persona.restore_version", Title: pageWord(r.l, "version_restore"),
			Action: &wire.Action{ID: "persona.restore_version", Confirm: i18n.T(r.l, "assistant-pages.version_confirm", i18n.Args{"number": revision})}})
	}
	v := wire.View{Title: versionLabel(r.l, revision), State: pageState{Form: "version", V: p.Version, Rev: revision}.encode(),
		Sections: []wire.Section{{Key: "persona.preview", Title: version.Title, Items: items}}}
	return r.historyView(v, "", "persona.history", "back_history")
}

// versionContents copies what was displayed, including detail pages, into plain text. Reconstructing
// text components keeps every old reply, form, link and nested action inert. A stored view is bounded
// by 64 KiB; split long content by characters to respect the UI text limit on every client.
func versionContents(saved *wire.View) []wire.Component {
	var lines []string
	line := func(parts ...string) {
		parts = slices.DeleteFunc(parts, func(part string) bool { return part == "" })
		if len(parts) > 0 {
			lines = append(lines, strings.Join(parts, " · "))
		}
	}
	page := func(v *wire.View) {
		for _, section := range v.Sections {
			line(section.Title)
			for _, item := range section.Items {
				line(item.Title, item.Label, item.Subtitle, item.Value, item.Detail, item.Text, item.Caption)
				if item.Ratio != nil {
					line(strconv.FormatFloat(*item.Ratio*100, 'f', -1, 64) + "%")
				}
				for _, fact := range item.Facts {
					line(fact.Label, fact.Value)
				}
				for _, stat := range item.Stats {
					line(stat.Label, stat.Value, stat.Detail)
				}
				for _, series := range item.Series {
					line(series.Name, item.Unit)
					for _, point := range series.Points {
						line(point.Label, cmp.Or(point.Display, strconv.FormatFloat(point.Value, 'g', -1, 64)))
					}
				}
				var columns []string
				for _, col := range item.Columns {
					columns = append(columns, col.Title)
				}
				line(columns...)
				for _, cells := range item.Rows {
					line(cells...)
				}
				for _, tile := range item.Tiles {
					line(tile.Title, tile.Subtitle, tile.Value)
				}
				for _, node := range item.Nodes {
					line(node.Title, node.Subtitle)
				}
				for _, entry := range item.Entries {
					line(entry.Path)
				}
				for _, hunk := range item.Hunks {
					line(hunk.Header)
					lines = append(lines, hunk.Lines...)
				}
			}
			line(section.Footer)
		}
	}
	page(saved)
	for _, name := range slices.Sorted(maps.Keys(saved.Pages)) {
		detail := saved.Pages[name]
		line(cmp.Or(detail.Title, name))
		page(&detail)
	}
	content := []rune(strings.Join(lines, "\n"))
	var out []wire.Component
	for len(content) > 0 {
		size := min(len(content), wire.StringMost)
		out = append(out, wire.Component{Type: wire.ComponentText, Key: fmt.Sprintf("persona.content.%d", len(out)), Text: string(content[:size])})
		content = content[size:]
	}
	return out
}

func (r *pageRequest) restoreVersion(ctx context.Context, p *db.AssistantPageRow) (extension.ActionResult, error) {
	restored, err := r.st.RestoreAssistantPageVersion(ctx, p.ID, r.state.V, r.state.Rev, r.device, r.now)
	switch {
	case errors.Is(err, db.ErrAssistantPageVersion):
		return r.versionHistory(ctx, p, pageWord(r.l, "version_changed"))
	case errors.Is(err, db.ErrAssistantPageVersionNotFound):
		return r.versionHistory(ctx, p, pageWord(r.l, "version_gone"))
	case errors.Is(err, db.ErrAssistantPageTitleTaken):
		return extension.ActionResult{Toast: pageWord(r.l, "title_taken")}, nil
	case errors.Is(err, db.ErrAssistantPageNotFound), errors.Is(err, db.ErrAssistantPageRemoved), errors.Is(err, db.ErrAssistantPageNotKept):
		return extension.ActionResult{}, pagesRefusal(http.StatusNotFound, "not_found", "not_found")
	case err != nil:
		return extension.ActionResult{}, err
	}
	r.s.cancelPageRefresh(p.ID)
	r.s.pagesTold(ctx, p.ID, true)
	if len(restored.Watch) > 0 && restored.OnOverview {
		r.s.pageChanged(overviewCardID(p.ID), pageContent)
	}
	toast := pageWord(r.l, "version_restored")
	if r.version >= 3 {
		return extension.ActionResult{Pop: true, Toast: toast}, nil
	}
	return r.versionHistory(ctx, restored, toast)
}
