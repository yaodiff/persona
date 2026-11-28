package kb

// An extension's own recordings (kb.put_recording, docs/extensions/extensions.md "Writing recordings and minutes"):
// Core, with or without the recording module. The write goes through the same commit as a phone's upload
// (commitRecording), its minutes through the same files the minutes round leaves (minutes.json and minutes.md), so the
// retention keeps them past recording_days (pruneRecordings: hasMinutes) and the searches read them.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/nwverse/persona/server/internal/recordingwire"
)

type (
	RecordingWrite       = recordingwire.RecordingWrite
	RecordingWriteResult = recordingwire.RecordingWriteResult
)

// ErrNotRecordingWriter: the recording is the phone's or another extension's.
var ErrNotRecordingWriter = recordingwire.ErrNotRecordingWriter

// PutExtensionRecording creates or updates recording w.ID as extension's own (storage key of the extension, as
// ExtensionWrite.Extension). The same write again changes nothing (Unchanged); a recording the user deleted stays
// deleted (ErrRecordingDeleted); one that is not the extension's is ErrNotRecordingWriter; a bad write wraps
// ErrBadRecording.
func (e *Engine) PutExtensionRecording(w RecordingWrite, extension string, now time.Time) (RecordingWriteResult, error) {
	if e.Store.ReadOnly() {
		return RecordingWriteResult{}, ErrReadOnly
	}
	if strings.TrimSpace(extension) == "" {
		return RecordingWriteResult{}, errors.New("an extension's recording needs the extension")
	}
	id, ok := RecordingID(string(w.ID))
	if !ok {
		return RecordingWriteResult{}, badRecording("id must be a UUID")
	}
	w.ID = recordingwire.ID(id)
	segs, err := e.checkRecordingWrite(&w)
	if err != nil {
		return RecordingWriteResult{}, err
	}
	sum, err := recordingWriteSum(w)
	if err != nil {
		return RecordingWriteResult{}, err
	}
	by := ExtensionWriter(extension)
	st := e.Store
	st.recMu.Lock()
	defer st.recMu.Unlock()
	if _, gone := st.tombstones()[id]; gone {
		return RecordingWriteResult{ID: recordingwire.ID(id)}, ErrRecordingDeleted
	}
	r, exists := st.Recording(id)
	if exists && (r.Writer == nil || r.Writer.By != by) {
		return RecordingWriteResult{ID: recordingwire.ID(id)}, ErrNotRecordingWriter
	}
	if exists && r.Writer.Sum == sum {
		return RecordingWriteResult{ID: recordingwire.ID(id), Revision: r.Revision, Unchanged: true, MinutesVersion: r.MinutesVersion}, nil
	}
	if !exists {
		r = &Recording{ID: id}
	}
	r.Revision++
	r.StartedAt, r.EndedAt, r.Duration = w.StartedAt.UTC(), nil, 0
	if !w.EndedAt.IsZero() {
		end := w.EndedAt.UTC()
		r.EndedAt, r.Duration = &end, end.Sub(r.StartedAt).Seconds()
	} else if n := len(segs); n > 0 {
		r.Duration = segs[n-1].End
	}
	r.Title, r.Language, r.Kind, r.Complete = oneLine(w.Title), strings.TrimSpace(w.Language), RecordingMeeting, true
	r.Segments, r.Chars = len(segs), recordingwire.RecordChars(segs)
	// The recording module neither matches it to a calendar item nor writes its summary: the extension said what it
	// was, and spends no model call of the user's.
	r.MatchedRev, r.SummarizedRev = r.Revision, r.Revision
	if w.Meeting != nil {
		it, _ := st.Item(string(w.Meeting.Kind), string(w.Meeting.ID))
		for _, old := range []*recordingwire.MeetingRef{r.Meeting, r.ImportedMeeting} {
			if old != nil {
				r.Items = removeStrings(r.Items, []string{old.Kind + "/" + old.ID})
			}
		}
		ref := &recordingwire.MeetingRef{Kind: it.Kind, ID: it.ID, Name: oneLine(it.Title)}
		if it.Kind == KindMeeting {
			r.Meeting, r.ImportedMeeting = nil, ref
		} else {
			r.Meeting, r.ImportedMeeting = ref, nil
		}
		r.Items = addUnique(r.Items, it.Kind+"/"+it.ID)
	}
	var link []string
	for _, slug := range w.Projects {
		if !slices.Contains(r.Excluded["projects"], slug) { // a link the user removed is theirs to make again
			link = append(link, slug)
		}
	}
	r.Projects = addUnique(r.Projects, link...)
	r.Writer = &recordingwire.RecordingWriter{By: by, Sum: sum, At: now.UTC()}
	r.Name = e.recordingName(r)
	r.Updated = now.UTC()
	var m *Minutes
	if w.Minutes != nil {
		m = e.extensionMinutes(r, w.Minutes, now)
		r.MinutesVersion, r.MinutesRev = m.Version, r.Revision
		r.MinutesFails, r.MinutesFailRev, r.MinutesTimeouts, r.MinutesRetryAt = 0, 0, 0, time.Time{}
	} else {
		r.MinutesRev = 0
	}
	recordingwire.PlanMinutes(r)
	if m != nil && r.MinutesState == "" && r.Chars == 0 {
		r.MinutesState = MinutesReady // minutes without a transcript: they are all there will be
	}
	if err := st.commitRecording(r, segs); err != nil {
		return RecordingWriteResult{ID: recordingwire.ID(id)}, err
	}
	if m != nil {
		if err := st.putExtensionMinutes(id, m); err != nil {
			return RecordingWriteResult{ID: recordingwire.ID(id)}, err
		}
	}
	for _, slug := range link {
		if p, ok := st.Project(slug); ok && !slices.Contains(p.Items, KindRecording+"/"+id) {
			p.Items = addUnique(p.Items, KindRecording+"/"+id)
			if err := st.putEntity(KindProject, p.Slug, p, &p.Tagging, false); err != nil {
				return RecordingWriteResult{ID: recordingwire.ID(id)}, err
			}
		}
	}
	st.SearchIndexPurge() // the previous minutes' and transcript's words leave the search index at its next update
	st.notify(KindRecording, id, "metadata")
	st.notify(KindRecording, id, "messages")
	st.notify(KindRecording, id, "summary")
	e.index(KindRecording, id, r.Name, r.Tagging, r.OneLine, r.StartedAt)
	return RecordingWriteResult{ID: recordingwire.ID(id), Revision: r.Revision, Created: !exists, MinutesVersion: r.MinutesVersion}, nil
}

// checkRecordingWrite normalizes w and says what is wrong with it; the transcript is returned sorted, without empty
// lines.
func (e *Engine) checkRecordingWrite(w *RecordingWrite) ([]RecordingSegment, error) {
	if w.StartedAt.IsZero() {
		return nil, badRecording("started_at is required")
	}
	if !w.EndedAt.IsZero() && w.EndedAt.Before(w.StartedAt) {
		return nil, badRecording("ended_at is before started_at")
	}
	if len(w.Transcript) > recordingwire.RecordingWriteMaxSegments {
		return nil, badRecording("at most %d transcript segments", recordingwire.RecordingWriteMaxSegments)
	}
	segs := make([]RecordingSegment, 0, len(w.Transcript))
	for _, sg := range w.Transcript {
		if sg.Start < 0 || sg.End < sg.Start {
			return nil, badRecording("a transcript segment ends before it starts")
		}
		if sg.Text, sg.N, sg.Source, sg.Echo, sg.Speaker, sg.SpeakerUnsure = strings.TrimSpace(sg.Text), 0, "", false, "", false; sg.Text != "" {
			segs = append(segs, sg)
		}
	}
	sort.SliceStable(segs, func(i, j int) bool { return segs[i].Start < segs[j].Start })
	w.Transcript = segs
	if w.Minutes == nil && len(segs) == 0 {
		return nil, badRecording("minutes or a transcript is required")
	}
	if m := w.Minutes; m != nil {
		if strings.TrimSpace(m.Markdown) == "" {
			return nil, badRecording("minutes.markdown is required")
		}
		most := recordingwire.RecordingWriteMaxItems
		if len(m.Attendees) > most || len(m.Points) > most || len(m.Decisions) > most || len(m.Actions) > most || len(m.Open) > most {
			return nil, badRecording("at most %d of each list in the minutes", most)
		}
		for _, a := range m.Actions {
			if strings.TrimSpace(a.ID) == "" || strings.TrimSpace(a.What) == "" || utf8.RuneCountInString(a.ID) > 64 {
				return nil, badRecording("each action needs an id of at most 64 characters and what")
			}
		}
	}
	if len(w.Projects) > recordingwire.RecordingWriteMaxProjects {
		return nil, badRecording("at most %d projects", recordingwire.RecordingWriteMaxProjects)
	}
	for i, slug := range w.Projects {
		p, ok := e.Store.Project(slug)
		if !ok {
			if p = e.FindProject(slug); p == nil {
				return nil, badRecording("no project %q", slug)
			}
		}
		w.Projects[i] = p.Slug
	}
	w.Projects = slices.Compact(slices.Sorted(slices.Values(w.Projects)))
	if mt := w.Meeting; mt != nil {
		if !mt.Kind.Valid() || strings.TrimSpace(string(mt.ID)) == "" {
			return nil, badRecording("meeting needs kind %q or %q and an id", KindEvent, KindMeeting)
		}
		if _, ok := e.Store.Item(string(mt.Kind), string(mt.ID)); !ok {
			return nil, badRecording("no %s item %q", mt.Kind, mt.ID)
		}
	}
	return segs, nil
}

// recordingWriteSum is the digest of a normalized write: the same write again has the same one.
func recordingWriteSum(w RecordingWrite) (string, error) {
	raw, err := json.Marshal(w)
	if err != nil {
		return "", err
	}
	h := sha256.Sum256(raw)
	return hex.EncodeToString(h[:]), nil
}

// extensionMinutes is minutes.json of in for r (r.Revision the one written now): the version after the stored one, the
// span and the to-dos already made of its actions kept.
func (e *Engine) extensionMinutes(r *Recording, in *recordingwire.RecordingWriteMinutes, now time.Time) *Minutes {
	zone := e.Store.Zone()
	start, end := minutesSpan(r, zone)
	m := &Minutes{Version: r.MinutesVersion + 1, Revision: r.Revision, GeneratedAt: now.UTC(),
		Title: oneLine(in.Title), Where: oneLine(in.Where), Attendees: in.Attendees, Points: in.Points,
		Decisions: in.Decisions, Actions: in.Actions, Open: in.Open,
		Language: strings.TrimSpace(in.Language), Markdown: strings.TrimSpace(in.Markdown), Start: start, End: end}
	if m.Title == "" {
		m.Title = r.Name
	}
	m.When = fmt.Sprintf("%s – %s", start.Format("2006-01-02 15:04"), end.Format("15:04"))
	if old, err := e.Store.ReadMinutes(r.ID); err == nil {
		m.Version = max(m.Version, old.Version+1)
		todos := map[string]string{} // an action the user made a to-do of keeps it
		for _, a := range old.Actions {
			if a.TodoID != "" {
				todos[a.ID] = a.TodoID
			}
		}
		m.Actions = slices.Clone(m.Actions)
		for i := range m.Actions {
			if m.Actions[i].TodoID == "" {
				m.Actions[i].TodoID = todos[m.Actions[i].ID]
			}
		}
	}
	return m
}

// putExtensionMinutes writes minutes.json and minutes.md of recording id (just committed; the caller holds recMu).
func (s *Store) putExtensionMinutes(id string, m *Minutes) error {
	raw, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	s.recFilesMu.Lock()
	defer s.recFilesMu.Unlock()
	s.metaMu.Lock()
	defer s.metaMu.Unlock()
	if _, err := os.Stat(s.path(KindRecording, id, "meta.json")); err != nil {
		return err
	}
	if err := writeFile(s.path(KindRecording, id, "minutes.json"), append(raw, '\n')); err != nil {
		return err
	}
	return writeFile(s.path(KindRecording, id, "minutes.md"), []byte(m.Markdown+"\n"))
}
