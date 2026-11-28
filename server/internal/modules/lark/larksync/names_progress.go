package larksync

import (
	"cmp"
	"encoding/json"
	"maps"
	"slices"
	"time"
)

// The sender-name backfill in rounds.
//
// One round of ResolveNamesWithFallback has its own budget (the binding's Budget). Every lookup is paced by the
// binding's limiter (famIM, 5 requests a second), so a round can make at most a few hundred calls. A backlog of
// several hundred unnamed external senders (one contact/v3 call each) and a few large groups (up to
// chatMembersPagesMost member pages each) needs more than that. A round that runs out of time therefore stops where
// it is and says so (NameResolveResult.Partial), and namesProgress (larksyncwire.SenderNamesProgressCursor) carries
// what it did to the next round:
//
//   - each large group's member list scan: the next page token, the pages read, whether the list ended, and which
//     senders every page read so far was checked for (seen). A later round resumes the scan only while every sender
//     it wants now is among seen; a new sender restarts the scan from the first page, since earlier pages were never
//     checked for them. A scan that ended without finding its senders is not read again for namesRetryAfter.
//   - the lookups that found nothing: contact/v3 users without a visible name, sample messages that carried no
//     name, mention scans around an hour of a chat. Each is retried after namesRetryAfter.
//
// Only progress is kept, never a member list: the names found go to the messages and the name book as before.

const (
	namesRetryAfter   = 7 * 24 * time.Hour // a lookup that found nothing, or a scan that ended, is tried again after this
	namesProgressMost = 5000               // entries of each kind kept, the newest first
	memberScansMost   = 500                // member list scans kept
)

// namesProgress is the stored progress of the sender-name backfill.
type namesProgress struct {
	Scans   map[string]*memberScan `json:"scans,omitempty"`   // chat id -> its member list scan
	Contact map[string]int64       `json:"contact,omitempty"` // open id -> when contact/v3 had no name for it (Unix s)
	Message map[string]int64       `json:"message,omitempty"` // sender id -> when its sample message carried no name
	Mention map[string]int64       `json:"mention,omitempty"` // chat id + "\x00" + hour -> when a mention scan there ended
}

// memberScan is one chat's member list scan across rounds.
type memberScan struct {
	Next  string   `json:"next,omitempty"`
	Pages int      `json:"pages,omitempty"`
	Done  bool     `json:"done,omitempty"`
	Seen  []string `json:"seen,omitempty"` // sorted sender ids every page read since At was checked for
	At    int64    `json:"at"`             // when the scan started (Unix s)
}

// parseNamesProgress reads a stored progress, dropping what is older than namesRetryAfter; anything unreadable is a
// fresh start (a round never fails on metadata).
func parseNamesProgress(raw string, now time.Time) *namesProgress {
	p := &namesProgress{}
	if raw != "" {
		_ = json.Unmarshal([]byte(raw), p)
	}
	cutoff := now.Add(-namesRetryAfter).Unix()
	for id, s := range p.Scans {
		if s == nil || s.At < cutoff {
			delete(p.Scans, id)
		}
	}
	for _, m := range []map[string]int64{p.Contact, p.Message, p.Mention} {
		for k, at := range m {
			if at < cutoff {
				delete(m, k)
			}
		}
	}
	return p
}

// tried says whether key of m found nothing within namesRetryAfter.
func tried(m map[string]int64, key string) bool {
	_, ok := m[key]
	return ok
}

// mark records that key found nothing at now.
func mark(m *map[string]int64, key string, now time.Time) {
	if *m == nil {
		*m = map[string]int64{}
	}
	(*m)[key] = now.Unix()
}

// scanFor is the stored scan of chatID usable for the senders wanted now (sorted), or nil when there is none or it
// was not checked for one of them.
func (p *namesProgress) scanFor(chatID string, wanted []string) *memberScan {
	s := p.Scans[chatID]
	if s == nil {
		return nil
	}
	for _, id := range wanted {
		if _, found := slices.BinarySearch(s.Seen, id); !found {
			return nil
		}
	}
	return s
}

// keepScan records l, a scan of chatID checked for seen (sorted) since startedAt.
func (p *namesProgress) keepScan(chatID string, l *chatMemberList, seen []string, startedAt int64) {
	if p.Scans == nil {
		p.Scans = map[string]*memberScan{}
	}
	p.Scans[chatID] = &memberScan{Next: l.next, Pages: l.pages, Done: l.done, Seen: seen, At: startedAt}
}

// encode is the progress to store, trimmed to its limits, or "" when there is nothing in it.
func (p *namesProgress) encode() string {
	trim := func(m map[string]int64, most int) map[string]int64 {
		if len(m) <= most {
			return m
		}
		keys := slices.Collect(maps.Keys(m))
		slices.SortFunc(keys, func(a, b string) int {
			return cmp.Or(cmp.Compare(m[b], m[a]), cmp.Compare(a, b))
		})
		out := make(map[string]int64, most)
		for _, k := range keys[:most] {
			out[k] = m[k]
		}
		return out
	}
	p.Contact = trim(p.Contact, namesProgressMost)
	p.Message = trim(p.Message, namesProgressMost)
	p.Mention = trim(p.Mention, namesProgressMost)
	if len(p.Scans) > memberScansMost {
		ats := map[string]int64{}
		for id, s := range p.Scans {
			ats[id] = s.At
		}
		kept := trim(ats, memberScansMost)
		for id := range p.Scans {
			if _, ok := kept[id]; !ok {
				delete(p.Scans, id)
			}
		}
	}
	if len(p.Scans) == 0 && len(p.Contact) == 0 && len(p.Message) == 0 && len(p.Mention) == 0 {
		return ""
	}
	raw, err := json.Marshal(p)
	if err != nil {
		return ""
	}
	return string(raw)
}
