package writers

import (
	"fmt"
	"math"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// refIndex is the position of photo n in refs, -1 when it is not there.
func refIndex(refs []PhotoRef, n int) int {
	return slices.IndexFunc(refs, func(r PhotoRef) bool { return r.N == n })
}

// photosHereLocked: recording id may get photo files: not deleted, its meta.json there (a write must never bring a
// deleted recording's directory back). The caller holds recFilesMu.
func (s *store) photosHereLocked(id string) bool {
	if gone := s.tombstoned(id); gone {
		return false
	}
	_, err := os.Stat(s.path(KindRecording, id, "meta.json"))
	return err == nil
}

// photoAt is at clamped into recording r: from its start to its ended_at, or to now while it records.
func photoAt(r *Recording, at float64, now time.Time) float64 {
	end := now
	if r.EndedAt != nil {
		end = *r.EndedAt
	}
	span := max(end.Sub(r.StartedAt).Seconds(), 0)
	if math.IsNaN(at) || at < 0 {
		return 0
	}
	return min(at, span)
}

// photosRecording is the recording rawID names, for the photo routes: ErrRecordingDeleted after a delete, ErrNotFound
// with no upload of it.
func (s *store) photosRecording(rawID string) (*Recording, error) {
	id, ok := RecordingID(rawID)
	if !ok {
		return nil, ErrNotFound
	}
	if gone := s.tombstoned(id); gone {
		return nil, ErrRecordingDeleted
	}
	r, ok := s.Recording(id)
	if !ok {
		return nil, ErrNotFound
	}
	return r, nil
}

// checkPhoto refuses what the HTTP layer should have: a photo PhotoAdd cannot store as it is.
func checkPhoto(u PhotoUpload) error {
	bad := func(f string, a ...any) error { return fmt.Errorf("%w: %s", ErrBadPhoto, fmt.Sprintf(f, a...)) }
	switch {
	case !uuidRe.MatchString(u.ID):
		return bad("id must be a lowercase UUID")
	case u.Mime != PhotoJPEG && u.Mime != PhotoPNG:
		return bad("mime must be %s or %s", PhotoJPEG, PhotoPNG)
	case len(u.Data) == 0 || u.Width <= 0 || u.Height <= 0:
		return bad("no image")
	case utf8.RuneCountInString(u.Text) > MaxPhotoText || utf8.RuneCountInString(u.Caption) > MaxPhotoCaption:
		return bad("text is at most %d characters, caption at most %d", MaxPhotoText, MaxPhotoCaption)
	case u.Digest == "":
		return bad("no digest")
	}
	return nil
}

// PhotoAdd stores a photo of recording rawID; created is false when it was there already (a retry: the stored photo).
// queue: it is to go into the room once the room is ready (the manager's decision: the room attends, and neither it
// nor the meeting has ended). Errors in the contract's order: ErrRecordingDeleted, ErrNotFound, the same id with
// another body ErrOpIDReused, an id the owner deleted ErrPhotoDeleted, ErrReadOnly, ErrNotMeeting (its kind is not
// meeting), ErrTranscriptExpired (trimmed), ErrPhotosFull. Minutes already written are made again, without a second
// push; MeetingChanged hears of PartPhotos. The manager wakes the minutes' writer after a created photo.
func (e *engine) PhotoAdd(rawID string, u PhotoUpload, queue bool) (Photo, bool, error) {
	r, err := e.Store.photosRecording(rawID)
	if err != nil {
		return Photo{}, false, err
	}
	if err := checkPhoto(u); err != nil {
		return Photo{}, false, err
	}
	p, created, err := e.photoAdd(r.ID, u, queue, time.Now())
	if created {
		e.meetingChanged(r.ID, []string{PartPhotos})
	}
	return p, created, err
}

func (e *engine) photoAdd(id string, u PhotoUpload, queue bool, now time.Time) (Photo, bool, error) {
	st := e.Store
	st.recMu.Lock()
	defer st.recMu.Unlock()
	// Again under recMu: a delete or a trim may have come first.
	if gone := st.tombstoned(id); gone {
		return Photo{}, false, ErrRecordingDeleted
	}
	r, ok := st.Recording(id)
	if !ok {
		return Photo{}, false, ErrNotFound
	}
	b, err := st.readPhotos(id)
	if err != nil {
		return Photo{}, false, err
	}
	if i := slices.IndexFunc(b.Photos, func(p storedPhoto) bool { return p.ID == u.ID }); i >= 0 {
		if b.Photos[i].Digest != u.Digest {
			return Photo{}, false, ErrOpIDReused
		}
		return b.Photos[i].Photo(st.citedPhotos(id)), false, nil
	}
	switch {
	case slices.ContainsFunc(b.Deleted, func(d PhotoRef) bool { return d.ID == u.ID }):
		return Photo{}, false, ErrPhotoDeleted
	case st.ReadOnly():
		return Photo{}, false, ErrReadOnly
	case r.Kind != RecordingMeeting:
		return Photo{}, false, ErrNotMeeting
	case r.Trimmed():
		return Photo{}, false, ErrTranscriptExpired
	case len(b.Photos) >= MaxPhotos:
		return Photo{}, false, ErrPhotosFull
	}
	p := storedPhoto{N: b.Next, ID: u.ID, At: photoAt(r, u.At, now), Mime: u.Mime, Width: u.Width, Height: u.Height,
		Text: strings.TrimSpace(u.Text), Caption: strings.TrimSpace(u.Caption), Queued: queue,
		AddedAt: now.UTC().Truncate(time.Second), Digest: u.Digest}
	b.Photos = append(b.Photos, p)
	b.Next++
	b.Rev++
	zone := st.Zone()
	st.recFilesMu.Lock()
	err = st.putPhotoLocked(id, p, u.Data, &b, r.StartedAt, zone)
	st.recFilesMu.Unlock()
	if err != nil {
		return Photo{}, false, err
	}
	if err := e.photosRemakeMinutes(r); err != nil {
		e.logf("recording %s: minutes after a photo: %v", id, err)
	}
	return p.Photo(st.citedPhotos(id)), true, nil
}

// putPhotoLocked writes photo p's image, then photos.json and photos.md of b (which lists it): a crash between leaves
// an unlisted file, which the next photo of that number replaces. The caller holds recFilesMu.
func (s *store) putPhotoLocked(id string, p storedPhoto, data []byte, b *photoBook, start time.Time, zone *time.Location) error {
	if !s.photosHereLocked(id) {
		return ErrNotFound
	}
	dir := s.path(KindRecording, id, photosDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := s.writeRecordingFile(s.path(KindRecording, id, photosDir, p.FileName()), data); err != nil {
		return err
	}
	return s.putPhotosLocked(id, b, start, zone)
}

// Photos are recording rawID's listed photos, oldest first, each cited as its current minutes say, and PartPhotos'
// revision; also on a read-only knowledge base. ErrRecordingDeleted after a delete, ErrNotFound with no upload of it.
func (e *engine) Photos(rawID string) ([]Photo, int64, error) {
	// Under recMu, as the minutes are stored: their writer moves the revision before the minutes it is for, and a list
	// read between the two would take the new revision with the old cited.
	e.Store.recMu.Lock()
	defer e.Store.recMu.Unlock()
	r, err := e.Store.photosRecording(rawID)
	if err != nil {
		return nil, 0, err
	}
	b, err := e.Store.readPhotos(r.ID)
	if err != nil {
		return nil, 0, err
	}
	cited := e.Store.citedPhotos(r.ID)
	out := make([]Photo, 0, len(b.Photos))
	for _, p := range b.Photos {
		out = append(out, p.Photo(cited))
	}
	return out, b.Rev, nil
}

// PhotosRev is PartPhotos' revision of recording rawID: 0 while it has no photos (or can't be read).
func (e *engine) PhotosRev(rawID string) int64 {
	id, ok := RecordingID(rawID)
	if !ok {
		return 0
	}
	e.Store.recMu.Lock() // as Photos: never a revision whose minutes are not written yet
	defer e.Store.recMu.Unlock()
	b, err := e.Store.readPhotos(id)
	if err != nil {
		return 0
	}
	return b.Rev
}

// PhotoTexts are the words of recording rawID's listed photos, oldest first (a photo with none has all three ""):
// what the minutes, kb_get, the follow-up's seed and meeting.answer read of them. None for an unknown recording.
func (e *engine) PhotoTexts(rawID string) []PhotoText {
	id, ok := RecordingID(rawID)
	if !ok {
		return nil
	}
	b, err := e.Store.readPhotos(id)
	if err != nil {
		return nil
	}
	var out []PhotoText
	for _, p := range b.Photos {
		out = append(out, p.Words())
	}
	return out
}

// PhotoDelete deletes photo n of recording rawID (DELETE …/photos/{n}): the minutes' items citing it go at once and the
// minutes are made again without a second push (not on a trimmed recording), then its entry and file go, and a photo
// that went into the room is to be withdrawn there (PhotosForRoom). false for a photo the owner deleted before (a
// repeat), once what its delete left undone is done (an error while its file can't be removed). Errors in the
// contract's order: ErrRecordingDeleted, ErrNotFound, ErrPhotoNotFound (no such n, or one a trim removed), ErrReadOnly.
// MeetingChanged hears of PartPhotos; the manager wakes the minutes' writer after a deletion.
func (e *engine) PhotoDelete(rawID string, n int) (bool, error) {
	r, err := e.Store.photosRecording(rawID)
	if err != nil {
		return false, err
	}
	deleted, err := e.photoDelete(r.ID, n)
	if deleted {
		e.meetingChanged(r.ID, []string{PartPhotos})
	}
	return deleted, err
}

func (e *engine) photoDelete(id string, n int) (bool, error) {
	st := e.Store
	st.recMu.Lock()
	defer st.recMu.Unlock()
	if gone := st.tombstoned(id); gone {
		return false, ErrRecordingDeleted
	}
	r, ok := st.Recording(id)
	if !ok {
		return false, ErrNotFound
	}
	b, err := st.readPhotos(id)
	if err != nil {
		return false, err
	}
	i := b.Index(n)
	again := refIndex(b.Deleted, n) >= 0
	switch {
	case i < 0 && !again:
		return false, ErrPhotoNotFound
	case st.ReadOnly():
		return false, ErrReadOnly
	}
	// The photo's words (its line of photos.md, the minutes' items citing it) leave the search index at its next
	// update, as any text deleted on purpose.
	defer st.SearchIndexPurge()
	// The minutes first, then the entry (photos.json, photos.md), then the file: a repeat (its answer lost, or a crash
	// or a failure part of the way) finds the photo deleted and finishes what is left before it answers false.
	if err := e.photoUncite(r, n, i >= 0); err != nil {
		return false, err
	}
	if i >= 0 {
		p := b.Photos[i]
		b.Photos = slices.Delete(b.Photos, i, i+1)
		b.Deleted = append(b.Deleted, PhotoRef{N: p.N, ID: p.ID})
		if p.Forwarded && !r.Trimmed() { // a trimmed recording's room is deleted with its copy
			b.Withdraw = append(b.Withdraw, PhotoRef{N: p.N, ID: p.ID})
		}
		b.Rev++
	}
	zone := st.Zone()
	st.recFilesMu.Lock()
	err = func() error {
		if !st.photosHereLocked(id) {
			return ErrNotFound
		}
		// The entry before the file: a listed photo always has its file. A repeat makes photos.md what photos.json
		// lists first: the delete it repeats may have stopped between the two.
		if i >= 0 {
			if err := st.putPhotosLocked(id, &b, r.StartedAt, zone); err != nil {
				return err
			}
		} else if _, err := st.putPhotosTextLocked(id, &b, r.StartedAt, zone); err != nil {
			return err
		}
		return st.removePhotoFileLocked(id, n)
	}()
	st.recFilesMu.Unlock()
	if err != nil {
		return false, err
	}
	return i >= 0, nil
}

// removePhotoFileLocked removes photo n's image from recording id's attachments/, of either type (a repeat of its
// delete no longer has its entry; no other photo gets its number). The caller holds recFilesMu.
func (s *store) removePhotoFileLocked(id string, n int) error {
	for _, mime := range []string{PhotoJPEG, PhotoPNG} {
		name := storedPhoto{N: n, Mime: mime}.FileName()
		if err := os.Remove(s.path(KindRecording, id, photosDir, name)); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

// photoUncite takes photo n, which is being deleted, out of recording r's minutes before the photo goes (the caller
// holds recMu, r read under it): the items citing it leave minutes.json and minutes.md at once, a version on, the
// actions numbered again (each keeps its to-do), so nothing read from the minutes from then on (a follow-up's seed,
// kb_get, the searches, meeting.answer) has its words; then they are made again as for any photo change
// (photosRemake), without a push. listed: the photo is still listed; a repeat of its delete makes them again only
// when they still cite it. A trimmed recording's minutes stay as they are (their item shows the photo removed).
func (e *engine) photoUncite(r *Recording, n int, listed bool) error {
	if r.Trimmed() {
		return nil
	}
	st := e.Store
	cited := false
	m, err := st.ReadMinutes(r.ID)
	switch {
	case err == nil && minutesPhotos(m)[n]:
		dropPhotos(m, func(p int) bool { return p == n })
		m.Version = max(m.Version, r.MinutesVersion) + 1
		m.Markdown = e.renderMinutes(m, r.StartedAt, st.Zone(), meetingSpeakersOf(r.Conv))
		if wrote, err := st.putMinutes(r.ID, m); err != nil {
			return err
		} else if !wrote {
			return ErrNotFound
		}
		r.MinutesVersion, cited = m.Version, true
	case err == nil:
		// A repeat (the photo listed still, or not): the delete it repeats may have stopped after minutes.json, before
		// minutes.md or meta.json. Finish them from minutes.json, which no longer cites the photo.
		md, _ := os.ReadFile(st.path(KindRecording, r.ID, "minutes.md"))
		if string(md) != strings.TrimSpace(m.Markdown)+"\n" || r.MinutesVersion < m.Version {
			if wrote, err := st.putMinutes(r.ID, m); err != nil {
				return err
			} else if !wrote {
				return ErrNotFound
			}
			r.MinutesVersion, cited = max(r.MinutesVersion, m.Version), true
		}
	}
	if !listed && !cited {
		return nil
	}
	if !e.photosRemake(r) && !cited {
		return nil
	}
	if err := st.putEntity(KindRecording, r.ID, r, &r.Tagging, false); err != nil {
		return err
	}
	if cited {
		st.notify(KindRecording, r.ID, "summary")
	}
	return nil
}

// photosRemakeMinutes has recording r's minutes made again after a photo was added (photosRemake), and writes r. The
// caller holds recMu, r read under it.
func (e *engine) photosRemakeMinutes(r *Recording) error {
	if !e.photosRemake(r) {
		return nil
	}
	return e.Store.putEntity(KindRecording, r.ID, r, &r.Tagging, false)
}

// photosRemake has recording r's minutes made again after a photo was added or deleted (remakeQuiet); whether r
// changed (the caller writes it).
func (e *engine) photosRemake(r *Recording) bool { return e.remakeQuiet(r) }

// remakeQuiet has recording r's minutes made again from what it has now (a photo added or deleted, its final
// transcript stored); whether r changed (the caller writes it). Minutes written are made again quietly: that
// generation is not pushed (MinutesQuiet). While a generation is pending (the first, or one the owner asked for) it is
// only invalidated, so that a model call running now does not settle it, and it is pushed as it would have been.
// Nothing for a trimmed recording (no transcript to make them from) or before the transcript is complete (the first
// minutes read it then).
func (e *engine) remakeQuiet(r *Recording) bool {
	if r.Trimmed() || !r.Complete || r.Chars == 0 || !isMeeting(r) {
		return false
	}
	switch r.MinutesState {
	case MinutesPending:
		// Only so that minutes read before the change don't settle them: their failures and waits stay as they are.
		quiet := quietMinutes(r)
		r.MinutesAsks++
		if quiet {
			r.MinutesQuiet = r.MinutesAsks
		}
	case MinutesReady, MinutesFailed:
		if !e.Store.hasMinutes(r.ID) {
			return false // never written: nothing to make again
		}
		remakeMinutes(r)
		r.MinutesQuiet = r.MinutesAsks
	default:
		return false
	}
	return true
}

// photoUpdate changes recording rawID's photos.json with fn under recMu, for the room's bookkeeping: fn says whether
// to write it and whether PartPhotos' revision moves (MeetingChanged hears of it then). ErrRecordingDeleted,
// ErrNotFound, ErrReadOnly, or fn's error.
func (e *engine) photoUpdate(rawID string, fn func(b *photoBook, r *Recording) (write, rev bool, err error)) error {
	r, err := e.Store.photosRecording(rawID)
	if err != nil {
		return err
	}
	rev, err := e.photoUpdateLocked(r.ID, fn)
	if rev && err == nil {
		e.meetingChanged(r.ID, []string{PartPhotos})
	}
	return err
}

func (e *engine) photoUpdateLocked(id string, fn func(b *photoBook, r *Recording) (write, rev bool, err error)) (bool, error) {
	st := e.Store
	st.recMu.Lock()
	defer st.recMu.Unlock()
	if gone := st.tombstoned(id); gone {
		return false, ErrRecordingDeleted
	}
	r, ok := st.Recording(id)
	if !ok {
		return false, ErrNotFound
	}
	if st.ReadOnly() {
		return false, ErrReadOnly
	}
	b, err := st.readPhotos(id)
	if err != nil {
		return false, err
	}
	write, rev, err := fn(&b, r)
	if err != nil || !write {
		return false, err
	}
	if rev {
		b.Rev++
	}
	zone := st.Zone()
	st.recFilesMu.Lock()
	defer st.recFilesMu.Unlock()
	if !st.photosHereLocked(id) {
		return false, ErrNotFound
	}
	return rev, st.putPhotosLocked(id, &b, r.StartedAt, zone)
}

// PhotosForRoom is what the room of recording rawID is still to get (the feeder, meeting_room.go): the photos queued
// for it, oldest first (Cited not set), and the photos deleted after they went into it, which it is to be told of.
// Nothing for an unknown recording; a trimmed one has neither.
func (e *engine) PhotosForRoom(rawID string) (queued []Photo, withdraw []PhotoRef) {
	id, ok := RecordingID(rawID)
	if !ok {
		return nil, nil
	}
	b, err := e.Store.readPhotos(id)
	if err != nil {
		return nil, nil
	}
	for _, p := range b.Photos {
		if p.Queued {
			queued = append(queued, p.Photo(nil))
		}
	}
	return queued, slices.Clone(b.Withdraw)
}

// PhotoSending records, before it is sent, that photo n of recording rawID is going into the room: forwarded from
// then on (its revision moves), still queued until PhotoForwarded. So a send whose answer is lost, or cut off by a
// restart, leaves a photo the room may have: a delete withdraws it there, the room may note it, and it is sent again
// under its id (the hub's once-only guard answers delivered, or in doubt). ErrPhotoNotFound when it is not listed
// (deleted since: it never goes in).
func (e *engine) PhotoSending(rawID string, n int) error {
	return e.photoUpdate(rawID, func(b *photoBook, _ *Recording) (bool, bool, error) {
		i := b.Index(n)
		if i < 0 {
			return false, false, ErrPhotoNotFound
		}
		if b.Photos[i].Forwarded {
			return false, false, nil
		}
		b.Photos[i].Forwarded = true
		return true, true, nil
	})
}

// PhotoForwarded records that photo n of recording rawID went into the room (PhotoSending before it): no longer
// queued; its revision stays. Nothing for one the owner deleted meanwhile (forwarded already, so withdrawn there);
// ErrPhotoNotFound for any other.
func (e *engine) PhotoForwarded(rawID string, n int) error {
	return e.photoUpdate(rawID, func(b *photoBook, _ *Recording) (bool, bool, error) {
		if i := b.Index(n); i >= 0 {
			if !b.Photos[i].Queued {
				return false, false, nil
			}
			b.Photos[i].Queued = false
			return true, false, nil
		}
		if refIndex(b.Deleted, n) < 0 {
			return false, false, ErrPhotoNotFound
		}
		return false, false, nil
	})
}

// PhotoWithdrawn records that the room was told photo n of recording rawID was withdrawn; its revision stays.
func (e *engine) PhotoWithdrawn(rawID string, n int) error {
	return e.photoUpdate(rawID, func(b *photoBook, _ *Recording) (bool, bool, error) {
		i := refIndex(b.Withdraw, n)
		if i < 0 {
			return false, false, nil
		}
		b.Withdraw = slices.Delete(b.Withdraw, i, i+1)
		return true, false, nil
	})
}

// PhotoNote sets the room's note on photo n of recording rawID (meeting_photo_note): only a listed photo that went into
// the room, ErrPhotoNotFound otherwise. Its revision moves; the minutes are not made again for it.
func (e *engine) PhotoNote(rawID string, n int, text string) error {
	text = strings.TrimSpace(text)
	if text == "" || utf8.RuneCountInString(text) > MaxPhotoNote {
		return fmt.Errorf("%w: a note is 1 to %d characters", ErrBadPhoto, MaxPhotoNote)
	}
	return e.photoUpdate(rawID, func(b *photoBook, _ *Recording) (bool, bool, error) {
		i := b.Index(n)
		if i < 0 || !b.Photos[i].Forwarded {
			return false, false, ErrPhotoNotFound
		}
		if b.Photos[i].Note == text {
			return false, false, nil
		}
		b.Photos[i].Note = text
		return true, true, nil
	})
}

// dropUnlistedPhotos takes out of m the items citing a photo that is not listed (deleted, or trimmed away, while the
// model wrote them); the actions are numbered again.
func dropUnlistedPhotos(m *Minutes, b photoBook) {
	dropPhotos(m, func(n int) bool { return b.Index(n) < 0 })
}

// dropPhotos takes out of m the items citing a photo gone says goes (all four sections), and the overview's bullets
// and conclusions citing it (an empty topic and group go with them, the overview once no topic is left); the actions
// are numbered again, each keeping its to-do, and settled debates remap any outcome.decision index into the remaining
// decisions (debates cite transcript lines only, so they stay). Not for a trimmed recording, whose minutes stay as they are.
func dropPhotos(m *Minutes, gone func(n int) bool) {
	cites := func(n int) bool { return n > 0 && gone(n) }
	m.Points = slices.DeleteFunc(m.Points, func(x MinutesPoint) bool { return cites(x.Photo) })
	var keptDecisions []MinutesItem
	remap := make([]int, len(m.Decisions))
	for i, d := range m.Decisions {
		if cites(d.Photo) {
			remap[i] = -1
			continue
		}
		remap[i] = len(keptDecisions)
		keptDecisions = append(keptDecisions, d)
	}
	m.Decisions = keptDecisions
	remapDebateDecisions(m, remap)
	m.Actions = slices.DeleteFunc(m.Actions, func(x MinutesAction) bool { return cites(x.Photo) })
	m.Open = slices.DeleteFunc(m.Open, func(x MinutesItem) bool { return cites(x.Photo) })
	dropOverviewPhotos(m, cites)
	for i := range m.Actions {
		m.Actions[i].ID = "a" + strconv.Itoa(i+1)
	}
}

// citedChanged: a listed photo of b is cited by one of before and after and not the other, or a photo the owner
// deleted is cited by before and not after (also the only photo the minutes cited).
func citedChanged(b photoBook, before, after map[int]bool) bool {
	for _, p := range b.Photos {
		if before[p.N] != after[p.N] {
			return true
		}
	}
	for _, d := range b.Deleted {
		if before[d.N] && !after[d.N] {
			return true
		}
	}
	return false
}
