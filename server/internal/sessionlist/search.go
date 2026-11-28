package sessionlist

import (
	"slices"
	"sort"
	"strconv"
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

// SearchContext is a search's view of the adapter. Document runs outside the index's lock, and several searches may
// run their Documents at the same time: it must only read the item it is given.
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
	// HitSessionID is the session the hit is in when it is not the row's own: with Options.Roots, a side session
	// folded under the row (Fold.SearchAs, #7944). Its Evidence is that session's text, so a client opens the row with
	// that side shown at the hit.
	HitSessionID string `json:"hit_session_id,omitempty"`
}
type searchRecord struct {
	doc   SearchDocument
	lower SearchDocument
}

// rankedHit is a row's best hit while a search ranks and while its result waits for its continuation pages. Its
// evidence is built only when a page returns the row, from snippet, the document the hit is in (nil for a hit
// without evidence): a broad search matches thousands of rows and returns a page of 40.
type rankedHit struct {
	SearchHit
	snippet *SearchDocument
}

// decodedDocument is a document a search decoded before it took the index's lock (decodeSearchDocuments).
type decodedDocument struct {
	record *searchRecord
	err    error
}

// searchDecodeRounds bounds the passes decodeSearchDocuments makes outside the lock: a row that changes during the
// last of them is decoded under the lock.
const searchDecodeRounds = 2

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
	var b strings.Builder
	b.WriteByte('1')
	for _, id := range ids {
		b.WriteString(strconv.Itoa(len(id)))
		b.WriteByte(':')
		b.WriteString(id)
	}
	return b.String()
}

func inAllowedMachines(spelled, machine string) bool {
	if spelled == "" {
		return true
	}
	for rest := spelled[1:]; rest != ""; {
		var id string
		id, rest = lengthPrefixed(rest)
		if id == machine {
			return true
		}
	}
	return false
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

// The exact TRAE selectors name two different harnesses. All other harness queries still match names and IDs by substring.
func matchesHarness(targets []string, id, label string) bool {
	if len(targets) == 0 {
		return true
	}
	for _, target := range targets {
		switch target {
		case "trae":
			if id == "trae" {
				return true
			}
		case "traex", "traecli", "trae-cli":
			if id == "traex" {
				return true
			}
		default:
			if strings.Contains(id, target) || strings.Contains(label, target) {
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

// searchCandidates calls visit with every item a search with o may read and the row a hit in it comes back as: the
// item itself, or with Roots, for a side session folded under a listed row (Fold.SearchAs, #7944), that row, naming
// the side. Authorization and private exclusions run here, before any document is decoded, matched, scored or
// counted, never as a projection of already paginated hits. It runs under the index's lock.
func (i *Index) searchCandidates(o Options, f filters, allowed map[string]bool, visit func(item, row *record, rowID, side string) error) error {
	for id, item := range i.items {
		if item.item.HiddenByDirectory && !f.IncludeHidden {
			continue
		}
		row, rowID, side := item, id, ""
		if fold := item.item.Fold; o.Roots && fold != nil && fold.SearchAs != "" && fold.Root != "" {
			if (f.NoPrivate || f.AgentsOnly) && item.item.Private ||
				f.AgentsOnly && item.item.Assistant ||
				(f.Manager != "" && item.owner != f.Manager) ||
				(o.AllowedMachines != nil && !allowed[item.item.Machine]) ||
				(f.ExcludePaneSource != "" && f.ExcludePaneSource == f.paneSource(item.item)) {
				continue
			}
			row, rowID, side = i.items[fold.SearchAs], fold.SearchAs, fold.SearchSession
		}
		if !f.matches(row) || (o.AllowedMachines != nil && !allowed[row.item.Machine]) {
			continue
		}
		if err := visit(item, row, rowID, side); err != nil {
			return err
		}
	}
	return nil
}

func allowedMachines(o Options) map[string]bool {
	allowed := map[string]bool{}
	for _, id := range o.AllowedMachines {
		allowed[id] = true
	}
	return allowed
}

// decodeSearchDocuments decodes the documents a search with o reads that their records do not hold yet, without
// holding the index's lock while it decodes: the first search after a restart decodes every row, hundreds of
// milliseconds that session pushes (Replace) and list reads would otherwise wait out. It takes the lock only to list
// the records, and searchResult stores what it decoded on them under the lock. Records replaced meanwhile are listed
// again, for at most searchDecodeRounds passes.
func (i *Index) decodeSearchDocuments(o Options) map[*record]decodedDocument {
	ctx := o.SearchContext
	if ctx == nil || ctx.Document == nil {
		return nil
	}
	f, allowed := o.filters(), allowedMachines(o)
	var decoded map[*record]decodedDocument
	for range searchDecodeRounds {
		var missing []*record
		listed := map[*record]bool{}
		need := func(r *record) {
			if r.searchDocument == nil && !listed[r] {
				if _, done := decoded[r]; !done {
					listed[r] = true
					missing = append(missing, r)
				}
			}
		}
		i.mu.Lock()
		i.initialize()
		if i.undecoded == 0 {
			i.mu.Unlock()
			break
		}
		_ = i.searchCandidates(o, f, allowed, func(item, row *record, _, _ string) error {
			need(item)
			need(row)
			return nil
		})
		i.mu.Unlock()
		if len(missing) == 0 {
			break
		}
		if decoded == nil {
			decoded = make(map[*record]decodedDocument, len(missing))
		}
		// A record's item never changes once Replace made it, so it is read here without the lock.
		for _, r := range missing {
			doc, err := ctx.Document(r.item)
			if err != nil {
				decoded[r] = decodedDocument{err: err}
				continue
			}
			decoded[r] = decodedDocument{record: &searchRecord{doc: doc, lower: normalizedDocument(doc)}}
		}
	}
	return decoded
}

// searchResult ranks every hit of a search with o. decoded holds the documents decodeSearchDocuments decoded for it
// outside the lock; any other a record lacks is decoded here, under the lock.
func (i *Index) searchResult(o Options, decoded map[*record]decodedDocument) (*result, error) {
	ctx := o.SearchContext
	if ctx == nil || ctx.Document == nil {
		return nil, failure("invalid_query", "runtime search context is unavailable")
	}
	q := parseSearch(o.Search)
	now := ctx.Now
	if now.IsZero() {
		now = i.now()
	}
	r := &result{counts: emptyCounts(i.revisionToken(), o.Roots), ordered: true, searchHits: map[string]rankedHit{}, searchTerms: q.terms}
	f := o.filters()
	document := func(record *record) (*searchRecord, error) {
		if record.searchDocument == nil {
			if d, ok := decoded[record]; ok {
				if d.err != nil {
					return nil, d.err
				}
				record.searchDocument = d.record
				i.undecoded--
				return d.record, nil
			}
			doc, err := ctx.Document(record.item)
			if err != nil {
				return nil, err
			}
			record.searchDocument = &searchRecord{doc: doc, lower: normalizedDocument(doc)}
			i.undecoded--
		}
		return record.searchDocument, nil
	}
	err := i.searchCandidates(o, f, allowedMachines(o), func(record, row *record, rowID, side string) error {
		d, err := document(record)
		if err != nil {
			return err
		}
		rd := d // the row's document, for the operators
		if row != record {
			if rd, err = document(row); err != nil {
				return err
			}
		}
		if d.doc.Assistant || rd.doc.Assistant {
			return nil
		}
		state := stateOf(row.item, o.Roots, o.CrossMachineHierarchy)
		if state == "" {
			state = rd.doc.State
		}
		name := strings.ToLower(ctx.MachineNames[row.item.Machine])
		if !anySearch(q.machines, strings.ToLower(row.item.Machine), name) || !anySearch(q.projects, rd.lower.Project, rd.lower.Cwd) || !matchesHarness(q.harnesses, rd.lower.Harness, rd.lower.HarnessLabel) || !anySearch(q.models, rd.lower.Model) || (len(q.states) > 0 && !slices.Contains(q.states, state)) || (q.pinned && !ctx.Pinned[rowID]) || (q.errors && !rd.doc.HasErrors && state != "needs_you" && !ctx.Errors[rowID]) {
			return nil
		}
		activeAt := record.item.UpdatedAt // when the text searched was written: a side's own time
		if activeAt.IsZero() {
			activeAt = d.doc.ActiveAt
		}
		if q.window > 0 && (activeAt.IsZero() || now.Sub(activeAt) > q.window) {
			return nil
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
					return nil
				}
				if allPlace {
					score = 60
				} else {
					score = 40
				}
			}
		}
		hit := rankedHit{SearchHit: SearchHit{Score: score, HitSessionID: side}}
		if score == 40 {
			hit.snippet = &d.doc
		}
		if state != "offline" {
			hit.Score += 5
		}
		if was, listed := r.searchHits[rowID]; listed {
			// The row is listed once, by its best hit: its own on a tie, else the side's first by id.
			if hit.Score > was.Score || hit.Score == was.Score && was.HitSessionID != "" &&
				(side == "" || side < was.HitSessionID) {
				r.searchHits[rowID] = hit
			}
			return nil
		}
		r.searchHits[rowID] = hit
		r.events = append(r.events, event{id: rowID, updated: row.item.UpdatedAt, value: row})
		addCount(&r.counts, row.item, f, 1)
		return nil
	})
	if err != nil {
		return nil, err
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
