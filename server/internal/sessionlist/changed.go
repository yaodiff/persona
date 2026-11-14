package sessionlist

import "slices"

// Changed lists the ids that changed after revision from up to and including revision to (two of this index's
// Revision tokens), sorted, at most max of them: a sessions_list hint's `changed` (#6685). ok is false when it cannot
// say: a token that is malformed, of another epoch, ahead of the index or no longer in the journal, to before from, or
// more than max ids.
func (i *Index) Changed(from, to string, max int) (ids []string, ok bool) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.initialize()
	start, err := i.sinceRevision(from)
	if err != nil {
		return nil, false
	}
	end, err := i.sinceRevision(to)
	if err != nil || end < start {
		return nil, false
	}
	seen := map[string]bool{}
	for _, entry := range i.journal {
		if entry.revision <= start || entry.revision > end {
			continue
		}
		for id := range entry.changes {
			if !seen[id] {
				if len(seen) == max {
					return nil, false
				}
				seen[id] = true
			}
		}
	}
	ids = make([]string, 0, len(seen))
	for id := range seen {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids, true
}
