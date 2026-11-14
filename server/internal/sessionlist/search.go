package sessionlist

import (
	"encoding/json"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode"
)

// SearchDocument is a runtime adapter's immutable metadata/last-answer projection,
// not a transcript. It is decoded on search intent, cached on a record and retired
// when that record changes. Ordinary lists pay no projection/allocation cost.
type SearchDocument struct {
	ActiveAt                                                 time.Time
	Title, Project, Cwd, Place, Harness, HarnessLabel, Model string
	UserText, AgentText, ErrorText, Metadata, State          string
	Assistant, HasErrors                                     bool
}
type SearchContext struct {
	Document       func(Item) (SearchDocument, error)
	MachineNames   map[string]string
	Pinned, Errors map[string]bool // composite runtime identities
	Now            time.Time
}
type SearchEvidence struct {
	Role   string `json:"role"`
	Before string `json:"before"`
	Match  string `json:"match"`
	After  string `json:"after"`
}
type SearchHit struct {
	Score    int             `json:"score"`
	Evidence *SearchEvidence `json:"evidence,omitempty"`
}
type searchRecord struct {
	doc   SearchDocument
	lower SearchDocument
}
type searchQuery struct {
	terms, machines, projects, harnesses, models, states []string
	text                                                 string
	pinned, errors                                       bool
	window                                               time.Duration
}

func allowedMachineKey(ids []string) string {
	if ids == nil {
		return ""
	}
	ids = slices.Clone(ids)
	slices.Sort(ids)
	ids = slices.Compact(ids)
	data, _ := json.Marshal(ids)
	return string(data)
}

// SearchNeedsPins lets adapters avoid a database read for queries that never
// inspect pins. A failed required read must be reported, not treated as no pins.
func SearchNeedsPins(raw string) bool { return parseSearch(raw).pinned }

func parseSearch(raw string) searchQuery {
	q := searchQuery{}
	for _, token := range strings.Fields(raw) {
		key, value, colon := strings.Cut(token, ":")
		key, value = strings.ToLower(key), strings.ToLower(value)
		used := colon && key != "" && value != ""
		if used {
			switch key {
			case "machine", "host":
				q.machines = append(q.machines, value)
			case "project", "repo", "cwd", "path":
				q.projects = append(q.projects, value)
			case "harness", "agent", "launcher":
				q.harnesses = append(q.harnesses, value)
			case "model":
				q.models = append(q.models, value)
			case "state", "status":
				if searchState(value) {
					q.states = append(q.states, value)
				} else {
					used = false
				}
			case "is":
				switch value {
				case "pin", "pinned":
					q.pinned = true
				case "error", "errors", "failed", "blocked":
					q.errors = true
				default:
					if value != "unknown" && searchState(value) {
						q.states = append(q.states, value)
					} else {
						used = false
					}
				}
			case "since", "after", "updated":
				switch value {
				case "24h", "1d", "2d", "48h":
					q.window = 24 * time.Hour // existing client aliases
				case "7d", "1w":
					q.window = 7 * 24 * time.Hour
				case "30d", "1m":
					q.window = 30 * 24 * time.Hour
				default:
					used = false
				}
			default:
				used = false
			}
		}
		if !used {
			q.terms = append(q.terms, strings.ToLower(token))
		}
	}
	q.text = strings.Join(q.terms, " ")
	return q
}
func searchState(s string) bool {
	return slices.Contains([]string{"needs_you", "working", "unread", "idle", "offline", "unknown"}, s)
}
func anySearch(targets []string, values ...string) bool {
	if len(targets) == 0 {
		return true
	}
	for _, target := range targets {
		for _, value := range values {
			if strings.Contains(value, target) {
				return true
			}
		}
	}
	return false
}
func normalizedDocument(d SearchDocument) SearchDocument {
	d.Title = strings.ToLower(d.Title)
	d.Project = strings.ToLower(d.Project)
	d.Cwd = strings.ToLower(d.Cwd)
	d.Place = strings.ToLower(d.Place)
	d.Harness = strings.ToLower(d.Harness)
	d.HarnessLabel = strings.ToLower(d.HarnessLabel)
	d.Model = strings.ToLower(d.Model)
	d.UserText = strings.ToLower(d.UserText)
	d.AgentText = strings.ToLower(d.AgentText)
	d.ErrorText = strings.ToLower(d.ErrorText)
	d.Metadata = strings.ToLower(d.Metadata)
	return d
}
func (i *Index) searchResult(o Options) (*result, error) {
	ctx := o.SearchContext
	if ctx == nil || ctx.Document == nil {
		return nil, failure("invalid_query", "runtime search context is unavailable")
	}
	q := parseSearch(o.Search)
	now := ctx.Now
	if now.IsZero() {
		now = i.now()
	}
	allowed := map[string]bool{}
	for _, id := range o.AllowedMachines {
		allowed[id] = true
	}
	r := &result{counts: emptyCounts(i.revisionToken()), ordered: true, searchHits: map[string]SearchHit{}}
	f := o.filters()
	for id, record := range i.items {
		// Authorization and private exclusions run before decoding, matching,
		// scoring or counting, never as a projection of already paginated hits.
		if !f.matches(record) || (o.AllowedMachines != nil && !allowed[record.item.Machine]) {
			continue
		}
		if record.searchDocument == nil {
			doc, err := ctx.Document(record.item)
			if err != nil {
				return nil, err
			}
			record.searchDocument = &searchRecord{doc: doc, lower: normalizedDocument(doc)}
		}
		d := record.searchDocument
		if d.doc.Assistant {
			continue
		}
		state := stateOf(record.item, o.Roots)
		if state == "" {
			state = d.doc.State
		}
		name := strings.ToLower(ctx.MachineNames[record.item.Machine])
		if !anySearch(q.machines, strings.ToLower(record.item.Machine), name) || !anySearch(q.projects, d.lower.Project, d.lower.Cwd) || !anySearch(q.harnesses, d.lower.Harness, d.lower.HarnessLabel) || !anySearch(q.models, d.lower.Model) || (len(q.states) > 0 && !slices.Contains(q.states, state)) || (q.pinned && !ctx.Pinned[id]) || (q.errors && !d.doc.HasErrors && state != "needs_you" && !ctx.Errors[id]) {
			continue
		}
		activeAt := record.item.UpdatedAt
		if activeAt.IsZero() {
			activeAt = d.doc.ActiveAt
		}
		if q.window > 0 && (activeAt.IsZero() || now.Sub(activeAt) > q.window) {
			continue
		}
		place := d.lower.Title + "\n" + d.lower.Place + "\n" + d.lower.Project + "\n" + strings.ToLower(record.item.Machine) + "\n" + name
		score := 30
		if len(q.terms) > 0 {
			switch {
			case strings.HasPrefix(d.lower.Title, q.text):
				score = 100
			case strings.Contains(d.lower.Title, q.text):
				score = 80
			default:
				allPlace, all := true, true
				for _, term := range q.terms {
					inPlace := strings.Contains(place, term)
					allPlace = allPlace && inPlace
					all = all && (inPlace || strings.Contains(d.lower.UserText, term) || strings.Contains(d.lower.AgentText, term) || strings.Contains(d.lower.ErrorText, term) || strings.Contains(d.lower.Metadata, term))
				}
				if !all {
					continue
				}
				if allPlace {
					score = 60
				} else {
					score = 40
				}
			}
		}
		hit := SearchHit{Score: score}
		if score == 40 {
			hit.Evidence = searchSnippet(d.doc, q.terms)
		}
		if state != "offline" {
			hit.Score += 5
		}
		r.searchHits[id] = hit
		r.events = append(r.events, event{id: id, updated: record.item.UpdatedAt, value: record})
		addCount(&r.counts, record.item, o.Roots, 1)
	}
	sort.Slice(r.events, func(a, b int) bool {
		x, y := r.events[a], r.events[b]
		if r.searchHits[x.id].Score != r.searchHits[y.id].Score {
			return r.searchHits[x.id].Score > r.searchHits[y.id].Score
		}
		if !x.updated.Equal(y.updated) {
			return x.updated.After(y.updated)
		}
		return x.id < y.id
	})
	return r, nil
}
func searchSnippet(doc SearchDocument, terms []string) *SearchEvidence {
	for _, source := range []struct{ role, text string }{{"error", doc.ErrorText}, {"user", doc.UserText}, {"agent", doc.AgentText}, {"agent", doc.Metadata}, {"title", doc.Title}} {
		runes := []rune(source.text)
		for _, term := range terms {
			needle := []rune(term)
			for at := 0; at+len(needle) <= len(runes); at++ {
				matches := true
				for n, ch := range needle {
					if unicode.ToLower(runes[at+n]) != ch {
						matches = false
						break
					}
				}
				if matches {
					end := at + len(needle)
					return &SearchEvidence{Role: source.role, Before: string(runes[max(0, at-64):at]), Match: string(runes[at:end]), After: string(runes[end:min(len(runes), end+160)])}
				}
			}
		}
	}
	return nil
}
