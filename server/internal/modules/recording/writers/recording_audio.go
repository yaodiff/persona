package writers

import (
	"cmp"
	"encoding/json"
	"errors"
	"io"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
)

// A hub-mode meeting's audio (docs/assistant/meeting-minutes.md "Audio from the phone", "Gaps and the final pass"):
// PCM16 little-endian mono at AudioRate, in recordings/<id>/attachments/audio/ as chunk files named by their first
// sample (<from>.pcm, contiguous, at most audioChunkMost samples each, never overlapping) and index.json, the spans
// held as of the last sync. The chunks' names and sizes are the truth (a crash between a chunk and the index loses
// nothing a sync acknowledged); the index is for people and tools. Knowledge-base backups never enter attachments/
// (kbhistory backupKB), operational backups exclude recordings/<id>/attachments/audio/ (operations/backup,
// update/webdev.py), and search never enters attachments/ either (find.go), so no backup taken during a meeting
// outlives the deletion below; nothing of it is in an upload's undo journal.
//
// The phone's socket writes through an AudioWriter, gap uploads through AudioPut; both store only what is not held.
// The final pass reads it back (AudioRead). It goes once the minutes made from the final transcript are written (a
// quiet remake counts), once its minutes end failed or it gets none (not a meeting, no text, never complete), with the
// trim and with the recording's delete; every retention pass deletes what should be gone (sweepTranscriptions).
//
// Locks: a recording's audio has its own mutex (audioRec.mu), taken before recFilesMu, which an append takes for
// reading only (a delete, the trim and the retention hold it for writing, so they wait for a write in flight and every
// write after them sees the directory gone); meetMu, when needed, is taken before audioRec.mu, never after.
// Lock order: recMu → meetMu → liveMu → audioRec.mu → recFilesMu → metaMu.

const (
	audioIndexFile = "index.json"
	// audioMost is the first sample the hub keeps no audio from (MaxAudioSeconds).
	audioMost = int64(MaxAudioSeconds) * AudioRate
)

// audioChunkMost is how many samples one chunk file holds at most (ten minutes; tests make it shorter).
var audioChunkMost = int64(10 * 60 * AudioRate)

// audioChunk is one chunk file: N samples from sample From.
type audioChunk struct {
	From int64 `json:"from"`
	N    int64 `json:"samples"`
}

func (c audioChunk) end() int64 { return c.From + c.N }

// chunkName is the file of the chunk starting at sample from.
func chunkName(from int64) string { return strconv.FormatInt(from, 10) + ".pcm" }

// chunkFrom is the first sample of the chunk file name, false when it is not one.
func chunkFrom(name string) (int64, bool) {
	digits, ok := strings.CutSuffix(name, ".pcm")
	if !ok || digits == "" || strings.TrimLeft(digits, "0123456789") != "" {
		return 0, false
	}
	n, err := strconv.ParseInt(digits, 10, 64)
	return n, err == nil && chunkName(n) == name
}

// audioIndex is index.json.
type audioIndex struct {
	Rate   int          `json:"rate"`
	Spans  [][2]float64 `json:"spans"`
	Chunks []audioChunk `json:"chunks"`
}

// audioRec is what the store knows of one recording's audio while this process runs.
type audioRec struct {
	mu      sync.Mutex
	loaded  bool
	chunks  []audioChunk // sorted, apart
	f       *os.File     // the chunk file appended to last, open for writing
	fFrom   int64
	newFile bool // a chunk file was made since the last sync (the directory is synced too)
	dirty   bool // index.json lags behind the chunks
	// end and gaps are meeting.json's End and gaps as last computed (audioGaps): a sync that changes the gaps writes
	// them to meeting.json.
	end   float64
	gaps  [][2]float64
	known bool
	// dropped is why the audio directory was removed (the recording deleted, trimmed, its audio deleted): every write
	// from then on is refused with it and every read finds nothing. Set under recFilesMu held for writing.
	dropped atomic.Pointer[error]
	// final: the final pass has started; writes are refused with ErrTranscriptionFinal, reads go on.
	final atomic.Bool
}

// audioRec is recording id's audio state, made on first use.
func (s *store) audioRec(id string) *audioRec {
	s.audioMapMu.Lock()
	defer s.audioMapMu.Unlock()
	if s.audioRecs == nil {
		s.audioRecs = map[string]*audioRec{}
	}
	rec := s.audioRecs[id]
	if rec == nil {
		rec = &audioRec{}
		s.audioRecs[id] = rec
	}
	return rec
}

// audioDroppedLocked notes that recording id's audio directory is being removed, why: writes in flight finish before
// (they hold recFilesMu for reading), and every one after is refused. The caller holds recFilesMu for writing.
func (s *store) audioDroppedLocked(id string, why error) {
	s.audioMapMu.Lock()
	rec := s.audioRecs[id]
	s.audioMapMu.Unlock()
	if rec != nil {
		rec.dropped.Store(&why)
	}
}

// audioFileClosed closes the chunk file recording id's audio has open for writing, if any: after a gap upload, and
// after a delete, a prune or the trim removed the directory (an open file would keep the unlinked audio on disk). The
// caller holds no recFilesMu (audioRec.mu comes before it).
func (s *store) audioFileClosed(id string) {
	s.audioMapMu.Lock()
	rec := s.audioRecs[id]
	s.audioMapMu.Unlock()
	if rec == nil {
		return
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	_ = rec.closeFileLocked() // synced before (AudioPut), or removed already
}

// audioFinal notes that recording id's final pass has started: its audio takes no more writes.
func (s *store) audioFinal(id string) { s.audioRec(id).final.Store(true) }

// audioPath is recording id's audio directory, or a file in it.
func (s *store) audioPath(id string, parts ...string) string {
	return s.path(KindRecording, id, append([]string{photosDir, audioDir}, parts...)...)
}

// gone is why rec's directory was removed, nil while it was not.
func (rec *audioRec) gone() error {
	if p := rec.dropped.Load(); p != nil {
		return *p
	}
	return nil
}

// loadLocked reads rec's chunks from the directory once: each <from>.pcm with its size in whole samples (a file of an
// odd size, the tail of a write a crash cut short, is cut back to them). The caller holds rec.mu and recFilesMu.
func (rec *audioRec) loadLocked(s *store, id string) error {
	if rec.loaded {
		return nil
	}
	entries, err := os.ReadDir(s.audioPath(id))
	if os.IsNotExist(err) {
		rec.chunks, rec.loaded = nil, true
		return nil
	}
	if err != nil {
		return err
	}
	var chunks []audioChunk
	for _, ent := range entries {
		from, ok := chunkFrom(ent.Name())
		if !ok || !ent.Type().IsRegular() {
			continue
		}
		info, err := ent.Info()
		if err != nil {
			return err
		}
		n := info.Size() / 2
		if info.Size()%2 != 0 && !s.readOnly {
			if err := os.Truncate(s.audioPath(id, ent.Name()), n*2); err != nil {
				return err
			}
		}
		if n > 0 {
			chunks = append(chunks, audioChunk{From: from, N: n})
		}
	}
	slices.SortFunc(chunks, func(a, b audioChunk) int { return cmp.Compare(a.From, b.From) })
	// Chunks never overlap; should a damaged directory have them do so, the earlier one counts only up to the next.
	for i := 1; i < len(chunks); i++ {
		if prev := &chunks[i-1]; prev.end() > chunks[i].From {
			prev.N = chunks[i].From - prev.From
		}
	}
	rec.chunks = slices.DeleteFunc(chunks, func(c audioChunk) bool { return c.N <= 0 })
	rec.loaded = true
	return nil
}

// spansLocked are the samples rec holds, as a union (touching chunks one span).
func (rec *audioRec) spansLocked() [][2]int64 {
	var out [][2]int64
	for _, c := range rec.chunks {
		if n := len(out); n > 0 && c.From <= out[n-1][1] {
			out[n-1][1] = max(out[n-1][1], c.end())
			continue
		}
		out = append(out, [2]int64{c.From, c.end()})
	}
	return out
}

// heldLocked are the spans rec holds in seconds.
func (rec *audioRec) heldLocked() [][2]float64 {
	spans := rec.spansLocked()
	if len(spans) == 0 {
		return nil
	}
	out := make([][2]float64, len(spans))
	for i, s := range spans {
		out[i] = [2]float64{sampleAt(s[0]), sampleAt(s[1])}
	}
	return out
}

// heldToLocked is the end of the latest span rec holds, in samples.
func (rec *audioRec) heldToLocked() int64 {
	if n := len(rec.chunks); n > 0 {
		return rec.chunks[n-1].end()
	}
	return 0
}

// missingLocked are the parts of samples [a, b) no chunk holds.
func (rec *audioRec) missingLocked(a, b int64) [][2]int64 {
	var out [][2]int64
	at := a
	for _, c := range rec.chunks {
		if c.end() <= at {
			continue
		}
		if c.From >= b {
			break
		}
		if c.From > at {
			out = append(out, [2]int64{at, c.From})
		}
		at = max(at, c.end())
		if at >= b {
			break
		}
	}
	if at < b {
		out = append(out, [2]int64{at, b})
	}
	return out
}

// fileLocked is the chunk file from (made empty when create), open for writing; the one open before is synced and
// closed first.
func (rec *audioRec) fileLocked(s *store, id string, from int64, create bool) (*os.File, error) {
	if rec.f != nil && rec.fFrom == from && !create {
		return rec.f, nil
	}
	if err := rec.closeFileLocked(); err != nil {
		return nil, err
	}
	flags := os.O_WRONLY
	if create {
		flags |= os.O_CREATE | os.O_TRUNC
	}
	f, err := os.OpenFile(s.audioPath(id, chunkName(from)), flags, 0o600)
	if err != nil {
		return nil, err
	}
	rec.f, rec.fFrom = f, from
	return f, nil
}

// closeFileLocked syncs and closes the chunk file open for writing, if any.
func (rec *audioRec) closeFileLocked() error {
	if rec.f == nil {
		return nil
	}
	err := errors.Join(rec.f.Sync(), rec.f.Close())
	rec.f = nil
	return err
}

// putLocked stores pcm from sample a, a span no chunk holds: appended to the chunk that ends at a while it has room,
// else in new chunk files. A new file is made only while the recording is there (no tombstone, its meta.json, not
// trimmed), so a write racing a delete or the trim never brings the directory back. The caller holds rec.mu and
// recFilesMu.
func (rec *audioRec) putLocked(s *store, id string, a int64, pcm []byte) error {
	for len(pcm) > 0 {
		n := int64(len(pcm) / 2)
		i := slices.IndexFunc(rec.chunks, func(c audioChunk) bool { return c.end() == a })
		var k int64
		if i >= 0 && rec.chunks[i].N < audioChunkMost {
			c := &rec.chunks[i]
			k = min(n, audioChunkMost-c.N)
			f, err := rec.fileLocked(s, id, c.From, false)
			if err != nil {
				return err
			}
			if _, err := f.WriteAt(pcm[:k*2], c.N*2); err != nil {
				_ = f.Truncate(c.N * 2)
				return err
			}
			c.N += k
		} else {
			switch {
			case !s.photosHereLocked(id):
				return ErrNotFound
			case s.trimmedLocked(id):
				return ErrTranscriptExpired
			}
			if err := os.MkdirAll(s.audioPath(id), 0o700); err != nil {
				return err
			}
			k = min(n, audioChunkMost)
			f, err := rec.fileLocked(s, id, a, true)
			if err != nil {
				return err
			}
			if _, err := f.WriteAt(pcm[:k*2], 0); err != nil {
				_ = f.Truncate(0)
				return err
			}
			at, _ := slices.BinarySearchFunc(rec.chunks, a, func(c audioChunk, t int64) int { return cmp.Compare(c.From, t) })
			rec.chunks = slices.Insert(rec.chunks, at, audioChunk{From: a, N: k})
			rec.newFile = true
		}
		rec.dirty = true
		a += k
		pcm = pcm[k*2:]
	}
	return nil
}

// syncLocked makes what rec stored durable: the open chunk file, the directory when a file was made, index.json.
func (rec *audioRec) syncLocked(s *store, id string) error {
	if rec.f != nil {
		if err := rec.f.Sync(); err != nil {
			return err
		}
	}
	if rec.newFile {
		syncRecordingDir(s.audioPath(id))
		syncRecordingDir(s.path(KindRecording, id, photosDir))
		rec.newFile = false
	}
	if !rec.dirty || len(rec.chunks) == 0 {
		return nil
	}
	held := rec.heldLocked()
	raw, err := json.MarshalIndent(audioIndex{Rate: AudioRate, Spans: held, Chunks: rec.chunks}, "", "  ")
	if err != nil {
		return err
	}
	if err := s.writeRecordingFile(s.audioPath(id, audioIndexFile), append(raw, '\n')); err != nil {
		return err
	}
	rec.dirty = false
	return nil
}

// audioWrite stores pcm (whole samples) of recording id from sample at, only what rec does not hold; end is the sample
// after the last one written. ErrAudioFull when some of it lies past audioMost (the rest is stored).
func (s *store) audioWrite(id string, rec *audioRec, at int64, pcm []byte) (end int64, err error) {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	s.recFilesMu.RLock()
	defer s.recFilesMu.RUnlock()
	switch {
	case rec.gone() != nil:
		return at, rec.gone()
	case rec.final.Load():
		return at, ErrTranscriptionFinal
	case s.readOnly:
		return at, ErrReadOnly
	}
	if err := rec.loadLocked(s, id); err != nil {
		return at, err
	}
	n, full := int64(len(pcm)/2), false
	if at+n > audioMost {
		n, full = max(0, audioMost-at), true
	}
	end = at + n
	for _, m := range rec.missingLocked(at, end) {
		if err := rec.putLocked(s, id, m[0], pcm[(m[0]-at)*2:(m[1]-at)*2]); err != nil {
			return end, err
		}
	}
	if full {
		return end, ErrAudioFull
	}
	return end, nil
}

// audioSync makes recording id's audio durable; ack is the end, in seconds, of the contiguous audio held around sample
// last (the latest write), and moved whether the gaps changed since meeting.json was given them.
func (s *store) audioSync(id string, rec *audioRec, last int64) (ack float64, moved bool, err error) {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	s.recFilesMu.RLock()
	defer s.recFilesMu.RUnlock()
	if err := rec.gone(); err != nil {
		return 0, false, err
	}
	if err := rec.loadLocked(s, id); err != nil {
		return 0, false, err
	}
	if err := rec.syncLocked(s, id); err != nil {
		return 0, false, err
	}
	ackAt := rec.heldToLocked()
	for _, sp := range rec.spansLocked() {
		if sp[0] <= last && last <= sp[1] {
			ackAt = sp[1]
			break
		}
	}
	held := rec.heldLocked()
	gaps := GapsIn(held, 0, gapsTo(held, rec.end))
	return sampleAt(ackAt), !rec.known || !sameSpans(gaps, rec.gaps), nil
}

// gapsTo is where a recording's gaps end: its end (rounded to a sample) once it is known, else the end of the latest
// audio held (during the meeting the gaps are those before ready.from).
func gapsTo(held [][2]float64, end float64) float64 {
	if end > 0 {
		return sampleAt(atSample(end))
	}
	if n := len(held); n > 0 {
		return held[n-1][1]
	}
	return 0
}

// audioGaps are recording id's gaps for a recording that ends at end (0: not known yet), from the audio held now;
// noted as what meeting.json was given. The caller may hold meetMu (never audioRec.mu).
func (e *engine) audioGaps(id string, end float64) [][2]float64 {
	st := e.Store
	rec := st.audioRec(id)
	rec.mu.Lock()
	defer rec.mu.Unlock()
	st.recFilesMu.RLock()
	defer st.recFilesMu.RUnlock()
	var held [][2]float64
	if rec.gone() == nil && rec.loadLocked(st, id) == nil {
		held = rec.heldLocked()
	}
	gaps := GapsIn(held, 0, gapsTo(held, end))
	rec.end, rec.gaps, rec.known = end, gaps, true
	return gaps
}

// audioHeldEnd is where the audio the hub holds of recording id ends, in seconds: 0 with none, or once it is gone. A
// hub-mode recording ends no earlier (#9587): a phone killed mid-segment loses the segment it was writing, which the
// hub got over the socket. The caller may hold meetMu (never audioRec.mu).
func (e *engine) audioHeldEnd(id string) float64 {
	st := e.Store
	rec := st.audioRec(id)
	rec.mu.Lock()
	defer rec.mu.Unlock()
	st.recFilesMu.RLock()
	defer st.recFilesMu.RUnlock()
	if rec.gone() != nil || rec.loadLocked(st, id) != nil {
		return 0
	}
	held := rec.heldLocked()
	if len(held) == 0 {
		return 0
	}
	return held[len(held)-1][1]
}

// refreshGaps writes recording id's gaps to meeting.json when the audio changed them (PartTranscription moves then),
// until the final pass starts; the gaps after it (meeting.json's once the final pass started).
func (e *engine) refreshGaps(id string) [][2]float64 {
	var gaps [][2]float64
	st := e.Store
	st.meetMu.Lock()
	_, parts, err := e.meetingUpdateLocked(id, func(m *Meeting, _ Recording) error {
		t := m.Transcription
		switch {
		case !t.Hub():
		case t.AudioClosed():
			gaps = t.Gaps
		default:
			t.Gaps = e.audioGaps(id, t.End)
			gaps = t.Gaps
			settleTranscription(t, e.now())
		}
		return nil
	})
	st.meetMu.Unlock()
	if err != nil {
		return e.audioGaps(id, 0)
	}
	e.meetingChanged(id, parts)
	return gaps
}

// audioFor is the recording rawID names for its audio, with the refusals in the socket's order: ErrRecordingDeleted,
// ErrNotFound, bad (PUT …/audio's ErrBadAudio, when not nil), ErrReadOnly, ErrNotHub, ErrTranscriptExpired (trimmed),
// ErrTranscriptionFinal (the final pass started or ran, or the audio is gone). Its audio state is made before the
// recording is read, so a delete from then on refuses the writes.
func (e *engine) audioFor(rawID string, bad error) (string, *audioRec, error) {
	id, ok := RecordingID(rawID)
	if !ok {
		return "", nil, ErrNotFound
	}
	st := e.Store
	if gone := st.tombstoned(id); gone {
		return "", nil, ErrRecordingDeleted
	}
	rec := st.audioRec(id)
	r, ok := st.Recording(id)
	switch {
	case !ok:
		if gone := st.tombstoned(id); gone {
			return "", nil, ErrRecordingDeleted
		}
		return "", nil, ErrNotFound
	case bad != nil:
		return "", nil, bad
	case st.ReadOnly():
		return "", nil, ErrReadOnly
	case !r.HubTranscribed():
		return "", nil, ErrNotHub
	case r.Trimmed():
		return "", nil, ErrTranscriptExpired
	case r.Conv.Transcription.AudioClosed():
		return "", nil, ErrTranscriptionFinal
	}
	return id, rec, nil
}

// AudioWriter stores one recording's audio from its socket; safe to close from another goroutine. A recording has one
// socket at a time; two writers of one recording would still never store a sample twice.
type AudioWriter struct {
	e      *engine
	id     string
	rec    *audioRec
	last   atomic.Int64 // the sample after the latest write
	closed atomic.Bool
}

// AudioOpen opens recording rawID's audio for writing. Its refusals, in the socket's order: ErrRecordingDeleted,
// ErrNotFound, ErrReadOnly, ErrNotHub, ErrTranscriptExpired, ErrTranscriptionFinal.
func (e *engine) AudioOpen(rawID string) (*AudioWriter, error) {
	id, rec, err := e.audioFor(rawID, nil)
	if err != nil {
		return nil, err
	}
	st := e.Store
	rec.mu.Lock()
	defer rec.mu.Unlock()
	st.recFilesMu.RLock()
	defer st.recFilesMu.RUnlock()
	switch {
	case rec.gone() != nil:
		return nil, rec.gone()
	case rec.final.Load():
		return nil, ErrTranscriptionFinal
	}
	if err := rec.loadLocked(st, id); err != nil {
		return nil, err
	}
	w := &AudioWriter{e: e, id: id, rec: rec}
	w.last.Store(rec.heldToLocked())
	return w, nil
}

// Write stores the audio of pcm (PCM16 little-endian mono at AudioRate) from recording second at (rounded to a
// sample), only what is not held yet: audio held already is ignored, audio past what is held leaves a gap between.
// ErrBadAudio for an odd length or a position that is not 0 or more; ErrAudioFull past MaxAudioSeconds (what lies
// before is stored); the recording's refusals once it was deleted, trimmed or its final pass started.
func (w *AudioWriter) Write(at float64, pcm []byte) error {
	if w.closed.Load() {
		return os.ErrClosed
	}
	s := atSample(at)
	if s < 0 || len(pcm)%2 != 0 {
		return ErrBadAudio
	}
	end, err := w.e.Store.audioWrite(w.id, w.rec, s, pcm)
	w.last.Store(end)
	return err
}

// Sync makes what was written durable and writes the index (and meeting.json's gaps when they moved); ackAt is the
// end of the contiguous audio held around the latest write (the socket's ack).
func (w *AudioWriter) Sync() (ackAt float64, err error) {
	if w.closed.Load() {
		return 0, os.ErrClosed
	}
	return w.sync()
}

func (w *AudioWriter) sync() (float64, error) {
	ack, moved, err := w.e.Store.audioSync(w.id, w.rec, w.last.Load())
	if err != nil {
		return 0, err
	}
	if moved {
		w.e.refreshGaps(w.id)
	}
	return ack, nil
}

// HeldTo is the end of the latest span held, in seconds (the socket's ready.from): 0 for a fresh recording.
func (w *AudioWriter) HeldTo() float64 {
	w.rec.mu.Lock()
	defer w.rec.mu.Unlock()
	if w.rec.gone() != nil {
		return 0
	}
	return sampleAt(w.rec.heldToLocked())
}

// Close syncs and closes the writer; closing it again is no error.
func (w *AudioWriter) Close() error {
	if w.closed.Swap(true) {
		return nil
	}
	_, err := w.sync()
	if gone := w.rec.gone(); gone != nil && errors.Is(err, gone) {
		err = nil // deleted meanwhile: nothing is left to sync
	}
	w.rec.mu.Lock()
	err = errors.Join(err, w.rec.closeFileLocked())
	w.rec.mu.Unlock()
	return err
}

// AudioHeld are the spans of recording rawID's audio the hub holds, in seconds (none once it is gone):
// ErrRecordingDeleted after a delete, ErrNotFound with no upload of it.
func (e *engine) AudioHeld(rawID string) ([][2]float64, error) {
	id, rec, err := e.audioReader(rawID)
	if err != nil {
		return nil, err
	}
	st := e.Store
	rec.mu.Lock()
	defer rec.mu.Unlock()
	st.recFilesMu.RLock()
	defer st.recFilesMu.RUnlock()
	if rec.gone() != nil {
		return nil, nil
	}
	if err := rec.loadLocked(st, id); err != nil {
		return nil, err
	}
	return rec.heldLocked(), nil
}

// audioReader is the recording rawID names for reading its audio: ErrRecordingDeleted, ErrNotFound.
func (e *engine) audioReader(rawID string) (string, *audioRec, error) {
	id, ok := RecordingID(rawID)
	if !ok {
		return "", nil, ErrNotFound
	}
	st := e.Store
	if gone := st.tombstoned(id); gone {
		return "", nil, ErrRecordingDeleted
	}
	if _, ok := st.Recording(id); !ok {
		return "", nil, ErrNotFound
	}
	return id, st.audioRec(id), nil
}

// AudioPut stores a gap upload's audio (PUT …/audio?at=) from recording second at, only what is not held, and
// returns the gaps left. Its refusals in the contract's order: ErrRecordingDeleted, ErrNotFound, ErrBadAudio (at not
// 0 or more, a body that is not whole samples), ErrReadOnly, ErrNotHub, ErrTranscriptExpired, ErrTranscriptionFinal;
// ErrAudioFull, with the gaps, when some of it lies past MaxAudioSeconds (the rest is stored).
func (e *engine) AudioPut(rawID string, at float64, pcm []byte) ([][2]float64, error) {
	s := atSample(at)
	var bad error
	if s < 0 || len(pcm)%2 != 0 {
		bad = ErrBadAudio
	}
	id, rec, err := e.audioFor(rawID, bad)
	if err != nil {
		return nil, err
	}
	st := e.Store
	// A gap upload leaves no chunk file open: a delete or the trim removes the directory without closing it, and an
	// unlinked file still open would keep the audio on disk (and its descriptor) until the manager restarts.
	defer st.audioFileClosed(id)
	_, werr := st.audioWrite(id, rec, s, pcm)
	if werr != nil && !errors.Is(werr, ErrAudioFull) {
		return nil, werr
	}
	if _, _, err := st.audioSync(id, rec, s); err != nil {
		return nil, err
	}
	gaps := e.refreshGaps(id)
	if gaps == nil {
		gaps = [][2]float64{}
	}
	return gaps, werr
}

// AudioRead reads the held audio of [from, from+len(p)/2/AudioRate) into p for the final pass, contiguous from from:
// n < len(p) where a hole starts (0 when from is in one), io.EOF when nothing is held at or after from (also once the
// audio is gone). ErrRecordingDeleted, ErrNotFound; ErrBadAudio for a position that is not 0 or more.
func (e *engine) AudioRead(rawID string, from float64, p []byte) (int, error) {
	s := atSample(from)
	if s < 0 {
		return 0, ErrBadAudio
	}
	id, rec, err := e.audioReader(rawID)
	if err != nil {
		return 0, err
	}
	st := e.Store
	rec.mu.Lock()
	defer rec.mu.Unlock()
	st.recFilesMu.RLock()
	defer st.recFilesMu.RUnlock()
	if rec.gone() != nil {
		return 0, io.EOF
	}
	if err := rec.loadLocked(st, id); err != nil {
		return 0, err
	}
	if rec.heldToLocked() <= s {
		return 0, io.EOF
	}
	want, read, at := int64(len(p)/2), int64(0), s
	for read < want {
		i := slices.IndexFunc(rec.chunks, func(c audioChunk) bool { return c.From <= at && at < c.end() })
		if i < 0 {
			break
		}
		c := rec.chunks[i]
		k := min(want-read, c.end()-at)
		f, err := os.Open(st.audioPath(id, chunkName(c.From)))
		if err != nil {
			return int(read * 2), err
		}
		_, err = f.ReadAt(p[read*2:(read+k)*2], (at-c.From)*2)
		f.Close()
		if err != nil {
			return int(read * 2), err
		}
		read, at = read+k, at+k
	}
	return int(read * 2), nil
}

// AudioDelete deletes recording rawID's audio and sets AudioGoneAt; deleting it again (or a recording deleted or
// trimmed) is no error. From then on the recording takes no audio (ErrTranscriptionFinal).
func (e *engine) AudioDelete(rawID string) error {
	id, ok := RecordingID(rawID)
	if !ok {
		return ErrNotFound
	}
	return e.dropAudio(id, ErrTranscriptionFinal)
}

// dropAudio removes recording id's audio directory (writes from then on refused with why) and notes it in
// meeting.json. The caller holds none of the store's locks.
func (e *engine) dropAudio(id string, why error) error {
	st := e.Store
	if st.ReadOnly() {
		return ErrReadOnly
	}
	rec := st.audioRec(id)
	rec.mu.Lock()
	st.recFilesMu.Lock()
	err := rec.closeFileLocked()
	if rerr := os.RemoveAll(st.audioPath(id)); rerr != nil {
		err = rerr
	} else {
		err = nil
		rec.dropped.Store(&why)
		rec.chunks, rec.loaded, rec.dirty, rec.newFile = nil, true, false, false
	}
	st.recFilesMu.Unlock()
	rec.mu.Unlock()
	if err != nil {
		return err
	}
	now := e.now()
	_, err = e.MeetingUpdate(id, func(m *Meeting, _ Recording) error {
		if t := m.Transcription; t.Hub() && t.AudioGoneAt.IsZero() {
			t.AudioGoneAt = now.UTC()
		}
		return nil
	})
	if errors.Is(err, ErrRecordingDeleted) || errors.Is(err, ErrNotFound) {
		return nil
	}
	return err
}

// ---- when the audio goes, and the minutes from the final transcript ----

// FinalTranscriptStored is told once the final pass stored its text (HubTranscriptPut with HubPassFinal) and the
// state is final or failed: pending minutes stop waiting (and minutes being made from the live text now don't settle
// them), minutes written are made again quietly (no second push); a recording that gets no minutes (not a meeting, no
// text, never complete, minutes failed) loses its audio now. The short summary needs no word: one not written from the
// final transcript is due from the state on (summaryDue). The caller wakes the minutes lane. ErrRecordingDeleted,
// ErrNotFound, ErrReadOnly, ErrNotHub.
func (e *engine) FinalTranscriptStored(rawID string) error {
	id, ok := RecordingID(rawID)
	if !ok {
		return ErrNotFound
	}
	st := e.Store
	if st.ReadOnly() {
		return ErrReadOnly
	}
	st.recMu.Lock()
	drop, err := e.finalStoredLocked(id)
	st.recMu.Unlock()
	if err != nil || !drop {
		return err
	}
	return e.dropAudio(id, ErrTranscriptionFinal)
}

// finalStoredLocked is FinalTranscriptStored under recMu; drop: the audio is to go now.
func (e *engine) finalStoredLocked(id string) (drop bool, err error) {
	st := e.Store
	r, ok := st.Recording(id)
	if !ok {
		if gone := st.tombstoned(id); gone {
			return false, ErrRecordingDeleted
		}
		return false, ErrNotFound
	}
	if !r.HubTranscribed() {
		return false, ErrNotHub
	}
	changed := e.remakeQuiet(r)
	drop = transcriptionDone(r) && st.noMinutesComing(r)
	if changed {
		err = st.putRecordingMeta(r)
	}
	return drop, err
}

// noMinutesComing: recording r gets no minutes from the transcript it has: not a meeting, no text, never complete, or
// its minutes failed with none written to make again.
func (s *store) noMinutesComing(r *Recording) bool {
	return !r.Complete || r.MinutesState == "" || r.MinutesState == MinutesFailed && !s.hasMinutes(r.ID)
}

// putRecordingMeta writes r's meta.json (putEntity), keeping r's meeting.json as read.
func (s *store) putRecordingMeta(r *Recording) error {
	conv := r.Conv
	err := s.putEntity(KindRecording, r.ID, r, &r.Tagging, false)
	r.Conv = conv
	return err
}

// audioDue: recording r's audio is to go now (the caller holds recMu, r read under it): it is not transcribed on the
// hub (an older manager's meeting.json after a rollback), or its final pass ran (or failed) and minutes made from that
// transcript were written, its minutes failed, or it gets none.
func (s *store) audioDue(r *Recording) bool {
	t := hubTranscription(r)
	switch {
	case t == nil:
		return true
	case !t.Done():
		return false
	case s.noMinutesComing(r) || r.MinutesState == MinutesFailed:
		return true
	case r.MinutesState == MinutesReady:
		m, err := s.ReadMinutes(r.ID)
		return err == nil && !m.GeneratedAt.Before(t.FinalAt)
	}
	return false
}

// sweepTranscriptions is every retention pass's backstop for what a crash between two steps left: audio that should
// be gone (audioDue) is deleted, minutes made before the final transcript of a recording whose final pass ran are
// made again (FinalTranscriptStored was never told), and a hub-mode recording still in the manager's hands gets its
// transcript.md and chars mended (repairRecord).
func (e *engine) sweepTranscriptions() {
	st := e.Store
	for _, r := range st.Recordings() {
		_, statErr := os.Stat(st.audioPath(r.ID))
		hasAudio := statErr == nil
		if t := hubTranscription(r); r.Trimmed() || !hasAudio && (t == nil || t.Done() && !t.AudioGoneAt.IsZero()) {
			continue // nothing of it is the sweep's (looked at again under recMu otherwise)
		}
		st.recMu.Lock()
		drop, why, changed := e.sweepOneLocked(r.ID, hasAudio)
		cur, ok := st.Recording(r.ID)
		st.recMu.Unlock()
		if changed && ok {
			st.notify(KindRecording, r.ID, "messages")
			e.index(KindRecording, r.ID, cur.Name, cur.Tagging, cur.OneLine, cur.StartedAt)
		}
		if drop {
			if err := e.dropAudio(r.ID, why); err != nil {
				e.logf("recording %s: audio: %v", r.ID, err)
			}
		}
	}
}

// sweepOneLocked is sweepTranscriptions for recording id under recMu: whether its audio goes (and why writes are
// refused from then on), whether its text or meta.json changed.
func (e *engine) sweepOneLocked(id string, hasAudio bool) (drop bool, why error, changed bool) {
	st := e.Store
	r, ok := st.Recording(id)
	if !ok || r.Trimmed() {
		return false, nil, false
	}
	t := hubTranscription(r)
	if t == nil {
		return hasAudio, ErrNotHub, false
	}
	if t.Done() && !t.AudioGoneAt.IsZero() && !hasAudio {
		return false, nil, false // settled
	}
	var err error
	if changed, err = e.repairRecord(r); err != nil {
		e.logf("recording %s: transcript of record: %v", id, err)
	}
	if !t.Done() {
		return false, nil, changed
	}
	if r.MinutesState == MinutesReady {
		if m, err := st.ReadMinutes(id); err == nil && m.GeneratedAt.Before(t.FinalAt) {
			// Minutes of the live text, and the final pass's never asked for (a crash before FinalTranscriptStored).
			d, err := e.finalStoredLocked(id)
			if err != nil {
				e.logf("recording %s: minutes from the final transcript: %v", id, err)
			}
			return d, ErrTranscriptionFinal, true
		}
	}
	return st.audioDue(r), ErrTranscriptionFinal, changed
}
