package assistant

// Transcription for extensions (#8485, docs/extensions/extensions.md "Transcription for extensions",
// schema/extension/transcription.schema.json): an extension granted the transcription scope hands its own audio, a
// piece at a time, to the transcription the owner chose for meetings, and gets the sentences back. The job runs on the
// contribution's replay mode, as a meeting's final pass does but with no transcript of record to check against: the
// contribution checks its sessions against each other, and a window where they do not agree comes back as a gap, never
// as a guess. One job per extension and extTranscribeMost across extensions at a time; meetings' jobs are not counted
// and not held up. The audio is never kept: it goes to the contribution as it comes. The log has one line a job (the
// extension, its label, the seconds, the outcome, the upstream's billed seconds), never the text or the audio.

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/bus"
	"github.com/nwverse/persona/server/internal/buswire"
	"github.com/nwverse/persona/server/internal/extwire"
	"github.com/nwverse/persona/server/internal/kb"
	"github.com/nwverse/persona/server/internal/rpc"
	"github.com/nwverse/persona/server/internal/transcribejob"
)

const (
	// extTranscribeMost is how many extensions' jobs run at once, so that they never take the upstream's room.
	extTranscribeMost = 2
	// extTranscribeChunkMost is the most PCM one transcription.audio carries (8 seconds).
	extTranscribeChunkMost = 256 << 10
	// extTranscribeLabelMost is how much of a job's label the log keeps (runes).
	extTranscribeLabelMost = 40
	// replayFrame is how much audio one binary frame carries (one second, under the 64 KiB a frame may be).
	replayFrame = kb.AudioRate * 2
)

// The times of a job, as a meeting's final pass has them (the recording module's meeting_transcribe.go); tests
// shorten them.
var (
	jobReadyWait = 30 * time.Second // a job answers ready within this, else it counts as upstream_unavailable
	jobDoneWait  = 5 * time.Minute  // a job sends done within this of the manager's end
	replayAhead  = time.Minute      // the most audio sent ahead of a job's last ack
)

// extTranscribeIdle: a job with no call for this long is cancelled (the extension went away); tests shorten it.
var extTranscribeIdle = time.Minute

// extTranscribeSamplesMost is the most audio a job takes: 15 minutes (tests shorten it).
var extTranscribeSamplesMost int64 = 15 * 60 * kb.AudioRate

// extTranscriptions are the extensions' jobs running now.
type extTranscriptions struct {
	mu   sync.Mutex
	jobs map[string]*extJob
}

// extJob is one extension's job.
type extJob struct {
	id, ext, label string
	ctx            context.Context // the job's: ended by finish, cancel, idleness or a failure
	stop           context.CancelFunc

	calls sync.Mutex // one of the job's calls at a time: its audio goes in order

	mu       sync.Mutex          // the fields below
	jc       *transcribejob.Conn // nil while it starts
	sent     int64               // samples sent
	acked    int64               // samples the contribution took
	heard    time.Time           // when the contribution last sent anything
	segments []assistantwire.TranscriptionSegment
	cur      []assistantwire.TranscriptionSegment // the open window's
	gaps     []assistantwire.TranscriptionGap
	usage    float64
	idle     *time.Timer
	ended    bool
	err      error         // why the contribution's side ended (nil: done)
	done     chan struct{} // closed when it ended
	wake     chan struct{}
}

// transcriptionRequest answers the manager's MethodTranscriptionExtension; false for any other method.
func (s *Service) transcriptionRequest(ctx context.Context, method string, params json.RawMessage) (any, bool, error) {
	if method != assistantwire.MethodTranscriptionExtension {
		return nil, false, nil
	}
	decode := func(out any) error {
		// The envelope accepts extra fields. Each transcription method decodes its own params strictly.
		call := out.(*assistantwire.ExtensionTranscription)
		if err := json.Unmarshal(params, call); err != nil || call.Extension == "" {
			return invalidTranscription(call.Method, "no extension")
		}
		return nil
	}
	return handleAssistantRequest(ctx, assistantwire.TranscriptionExtensionMethod, decode, s.extensionTranscription)
}

func (s *Service) extensionTranscription(ctx context.Context, call assistantwire.ExtensionTranscription) (json.RawMessage, error) {
	switch call.Method {
	case assistantwire.TranscriptionStartMethod.Name:
		return handleTranscriptionMethod(ctx, assistantwire.TranscriptionStartMethod, call, s.extTranscribeStart)
	case assistantwire.TranscriptionAudioMethod.Name:
		return handleTranscriptionMethod(ctx, assistantwire.TranscriptionAudioMethod, call, s.extTranscribeAudio)
	case assistantwire.TranscriptionFinishMethod.Name:
		return handleTranscriptionMethod(ctx, assistantwire.TranscriptionFinishMethod, call, s.extTranscribeFinish)
	case assistantwire.TranscriptionCancelMethod.Name:
		return handleTranscriptionMethod(ctx, assistantwire.TranscriptionCancelMethod, call, s.extTranscribeCancel)
	}
	return nil, invalidTranscription(call.Method, "no such method")
}

func handleTranscriptionMethod[P, R any](ctx context.Context, method buswire.Method[P, R], call assistantwire.ExtensionTranscription,
	run func(context.Context, string, P) (R, error)) (json.RawMessage, error) {
	var params P
	if err := strictParams(method.Name, call.Params, &params); err != nil {
		return nil, err
	}
	out, err := run(ctx, call.Extension, params)
	if err != nil {
		return nil, err
	}
	return rpc.Marshal(out)
}

func invalidTranscription(method, format string, args ...any) error {
	return &rpc.Error{Code: bus.CodeInvalidParams, Message: method + ": " + fmt.Sprintf(format, args...)}
}

func transcriptionError(code, format string, args ...any) error {
	return &rpc.Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

func (s *Service) extTranscribeStart(ctx context.Context, extension string, p assistantwire.TranscriptionStartParams) (assistantwire.TranscriptionStartResult, error) {
	var b [16]byte
	_, _ = rand.Read(b[:])
	jctx, stop := context.WithCancel(context.WithoutCancel(ctx))
	job := &extJob{id: hex.EncodeToString(b[:]), ext: extension, label: transcriptionLabel(p.Label), ctx: jctx, stop: stop,
		heard: time.Now(), done: make(chan struct{}), wake: make(chan struct{}, 1)}
	t := &s.extTr
	t.mu.Lock()
	running := 0
	for _, j := range t.jobs {
		if j.ext == extension {
			t.mu.Unlock()
			stop()
			return assistantwire.TranscriptionStartResult{}, transcriptionError(assistantwire.CodeTranscriptionBusy, "transcription.start: this extension has a job running; finish or cancel it first")
		}
		running++
	}
	if running >= extTranscribeMost {
		t.mu.Unlock()
		stop()
		return assistantwire.TranscriptionStartResult{}, transcriptionError(assistantwire.CodeTranscriptionBusy, "transcription.start: %d extensions' jobs are running; try again later", running)
	}
	if t.jobs == nil {
		t.jobs = map[string]*extJob{}
	}
	t.jobs[job.id] = job // its place is held while it starts
	t.mu.Unlock()

	jc, err := s.extTranscribeDial(ctx, p.Language)
	if err != nil {
		s.endExtJob(job, "unavailable")
		return assistantwire.TranscriptionStartResult{}, err
	}
	job.mu.Lock()
	job.jc = jc
	job.idle = time.AfterFunc(extTranscribeIdle, func() { s.endExtJob(job, "cancelled: no call for a minute") })
	job.mu.Unlock()
	go s.extTranscribeRead(job)
	return assistantwire.TranscriptionStartResult{Job: job.id}, nil
}

// extTranscribeDial opens a replay job on the chosen contribution and waits for it to be ready; every way it cannot is
// transcription_unavailable.
func (s *Service) extTranscribeDial(ctx context.Context, language string) (*transcribejob.Conn, error) {
	unavailable := func(why string, args ...any) error {
		return transcriptionError(assistantwire.CodeTranscriptionUnavailable, "transcription.start: "+why, args...)
	}
	if s.trans == nil {
		return nil, unavailable("no transcription in this manager")
	}
	ref, _, err := s.transcriptionTarget(ctx)
	if err != nil {
		return nil, unavailable("%v", err)
	}
	ictx, cancel := context.WithTimeout(ctx, transcriptionInfoTimeout)
	info, err := s.trans.TranscriptionInfo(ictx, ref)
	cancel()
	switch {
	case err != nil:
		return nil, unavailable("the transcription does not answer: %v", err)
	case info.Protocol != extwire.TranscriptionProtocol || info.Setup != "":
		return nil, unavailable("the transcription is not set up (Settings › Meetings)")
	case !slices.Contains(info.Features, extwire.TranscriptionReplay):
		return nil, unavailable("the transcription chosen cannot transcribe recorded audio (no replay)")
	}
	rctx, rcancel := context.WithTimeout(context.WithoutCancel(ctx), jobReadyWait)
	defer rcancel()
	jc, err := s.dialJob(rctx, ref)
	if err != nil {
		return nil, unavailable("%v", err)
	}
	start := extwire.TranscriptionFrame{Type: extwire.TFStart, Mode: extwire.TranscriptionReplay, Speed: 4, Language: language,
		At: extwire.F(0.0)}
	if err := jc.Send(rctx, start); err != nil {
		jc.Close()
		return nil, unavailable("%v", err)
	}
	if _, err := jc.AwaitReady(rctx); err != nil {
		jc.Close()
		return nil, unavailable("%v", err)
	}
	return jc, nil
}

// extTranscribeRead reads the job's frames until it is done, failed or ended.
func (s *Service) extTranscribeRead(job *extJob) {
	job.mu.Lock()
	jc := job.jc
	job.mu.Unlock()
	for {
		f, err := jc.Read(job.ctx)
		job.mu.Lock()
		if err != nil {
			job.endLocked(err)
			job.mu.Unlock()
			return
		}
		job.heard = time.Now()
		switch f.Type {
		case extwire.TFSentence:
			if f.Start != nil && f.End != nil && f.Text != nil && *f.Text != "" {
				job.cur = append(job.cur, assistantwire.TranscriptionSegment{From: *f.Start, To: max(*f.End, *f.Start), Text: *f.Text})
			}
		case extwire.TFWindow:
			if f.From != nil && f.To != nil {
				if f.Kept { // no session trusted: with no text of record to keep, a gap
					job.gaps = append(job.gaps, assistantwire.TranscriptionGap{From: *f.From, To: *f.To})
				} else {
					job.segments = append(job.segments, job.cur...)
				}
				job.cur = nil
			}
		case extwire.TFAck:
			if f.At != nil {
				job.acked = max(job.acked, int64(*f.At*kb.AudioRate))
				job.signalLocked()
			}
		case extwire.TFUsage:
			job.usage = max(job.usage, f.Seconds)
		case extwire.TFWarning:
			s.logf("extension %s: transcription warning %s", job.ext, f.Code)
		case extwire.TFError:
			if f.Fatal {
				job.endLocked(&transcribejob.Error{Code: f.Code})
				job.mu.Unlock()
				return
			}
			s.logf("extension %s: transcription error %s", job.ext, f.Code)
		case extwire.TFDone:
			job.segments = append(job.segments, job.cur...) // sentences after the last window
			job.cur = nil
			job.endLocked(nil)
			job.mu.Unlock()
			return
		}
		job.mu.Unlock()
	}
}

// endLocked: the contribution's side ended (err nil: done).
func (j *extJob) endLocked(err error) {
	select {
	case <-j.done:
		return
	default:
	}
	j.err = err
	close(j.done)
	j.signalLocked()
}

func (j *extJob) signalLocked() {
	select {
	case j.wake <- struct{}{}:
	default:
	}
}

// failed is the error a call answers once the contribution's side ended with err.
func (j *extJob) failed(method string, err error) error {
	var je *transcribejob.Error
	if errors.As(err, &je) {
		data, _ := json.Marshal(map[string]string{"code": je.Code})
		return &rpc.Error{Code: assistantwire.CodeTranscriptionFailed, Message: method + ": the transcription failed the job (" + je.Code + ")", Data: data}
	}
	return transcriptionError(assistantwire.CodeTranscriptionFailed, "%s: the transcription's connection ended", method)
}

// lookup is job id of extension ext while it runs; its calls are serialized (the caller unlocks calls).
func (s *Service) extTranscribeJob(method, ext, id string) (*extJob, error) {
	s.extTr.mu.Lock()
	job := s.extTr.jobs[id]
	s.extTr.mu.Unlock()
	if job == nil || job.ext != ext {
		return nil, invalidTranscription(method, "no such job (finished, cancelled, or not this extension's)")
	}
	job.calls.Lock()
	job.mu.Lock()
	ended, jc := job.ended, job.jc
	if job.idle != nil {
		job.idle.Stop() // Idle means no call in flight, not no frame for one bounded provider wait.
	}
	job.mu.Unlock()
	if ended || jc == nil {
		job.calls.Unlock()
		return nil, invalidTranscription(method, "no such job (finished, cancelled, or not this extension's)")
	}
	return job, nil
}

// extTranscribeCallDone releases one serialized call and restarts its no-call deadline only while the job lives.
// Finish and every error path end the job without rearming this timer.
func (s *Service) extTranscribeCallDone(job *extJob) {
	job.mu.Lock()
	if job.idle != nil && !job.ended {
		job.idle.Reset(extTranscribeIdle)
	}
	job.mu.Unlock()
	job.calls.Unlock()
}

func (s *Service) extTranscribeAudio(ctx context.Context, extension string, p assistantwire.TranscriptionAudioParams) (buswire.None, error) {
	const method = assistantwire.MethodTranscriptionAudio
	if base64.StdEncoding.DecodedLen(len(p.Data)) > extTranscribeChunkMost+3 {
		return buswire.None{}, invalidTranscription(method, "at most %d bytes of PCM a call", extTranscribeChunkMost)
	}
	pcm, err := base64.StdEncoding.DecodeString(p.Data)
	switch {
	case err != nil:
		return buswire.None{}, invalidTranscription(method, "data is not base64")
	case len(pcm) > extTranscribeChunkMost:
		return buswire.None{}, invalidTranscription(method, "at most %d bytes of PCM a call", extTranscribeChunkMost)
	case len(pcm)%2 != 0:
		return buswire.None{}, invalidTranscription(method, "PCM16: an even number of bytes")
	}
	job, err := s.extTranscribeJob(method, extension, p.Job)
	if err != nil {
		return buswire.None{}, err
	}
	defer s.extTranscribeCallDone(job)
	job.mu.Lock()
	sent := job.sent
	job.mu.Unlock()
	if sent+int64(len(pcm)/2) > extTranscribeSamplesMost {
		s.endExtJob(job, "too long")
		return buswire.None{}, transcriptionError(assistantwire.CodeTranscriptionTooLong, "%s: more than %d minutes of audio: the job is ended", method,
			extTranscribeSamplesMost/kb.AudioRate/60)
	}
	ahead := int64(replayAhead.Seconds() * kb.AudioRate)
	for len(pcm) > 0 {
		n := min(len(pcm), replayFrame)
		if err := job.waitRoom(ctx, ahead); err != nil {
			return buswire.None{}, s.extTranscribeFailed(job, method, err)
		}
		job.mu.Lock()
		jc := job.jc
		job.mu.Unlock()
		if err := jc.SendAudio(job.ctx, pcm[:n]); err != nil {
			return buswire.None{}, s.extTranscribeFailed(job, method, err)
		}
		job.mu.Lock()
		job.sent += int64(n / 2)
		job.mu.Unlock()
		pcm = pcm[n:]
	}
	return buswire.None{}, nil
}

// errStalled: the contribution answered nothing for jobDoneWait.
var errStalled = errors.New("the transcription stopped answering")

// waitRoom waits until no more than ahead samples wait past the contribution's last ack.
func (j *extJob) waitRoom(ctx context.Context, ahead int64) error {
	for {
		j.mu.Lock()
		behind, heard := j.sent-j.acked, j.heard
		select {
		case <-j.done:
			err := j.err
			j.mu.Unlock()
			if err == nil {
				err = errors.New("the transcription ended the job early")
			}
			return err
		default:
		}
		j.mu.Unlock()
		if behind <= ahead {
			return nil
		}
		left := jobDoneWait - time.Since(heard)
		if left <= 0 {
			return errStalled
		}
		t := time.NewTimer(left)
		select {
		case <-j.wake:
		case <-t.C:
		case <-ctx.Done():
			t.Stop()
			return ctx.Err()
		}
		t.Stop()
	}
}

// extTranscribeFailed ends job after err in method, and is the error the call answers.
func (s *Service) extTranscribeFailed(job *extJob, method string, err error) error {
	switch {
	case errors.Is(err, errStalled):
		s.endExtJob(job, "timed out")
		return transcriptionError(assistantwire.CodeTranscriptionTimeout, "%s: %v: the job is ended", method, err)
	case errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded):
		s.endExtJob(job, "the extension stopped waiting")
		return err
	}
	// A write that failed because the contribution failed the job and closed: its error frame says why, and the reader
	// has it once it reads that far.
	t := time.NewTimer(time.Second)
	select {
	case <-job.done:
	case <-t.C:
	}
	t.Stop()
	job.mu.Lock()
	var je *transcribejob.Error
	if errors.As(job.err, &je) {
		err = job.err
	}
	job.mu.Unlock()
	s.endExtJob(job, "failed")
	return job.failed(method, err)
}

func (s *Service) extTranscribeFinish(ctx context.Context, extension string, p assistantwire.TranscriptionJobParams) (assistantwire.TranscriptionFinishResult, error) {
	const method = assistantwire.MethodTranscriptionFinish
	job, err := s.extTranscribeJob(method, extension, p.Job)
	if err != nil {
		return assistantwire.TranscriptionFinishResult{}, err
	}
	defer s.extTranscribeCallDone(job)
	job.mu.Lock()
	if job.idle != nil {
		job.idle.Stop() // finishing: it ends here, however long the transcription takes
	}
	jc := job.jc
	job.mu.Unlock()
	if err := jc.Send(job.ctx, extwire.TranscriptionFrame{Type: extwire.TFEnd}); err != nil {
		return assistantwire.TranscriptionFinishResult{}, s.extTranscribeFailed(job, method, err)
	}
	for {
		job.mu.Lock()
		heard := job.heard
		job.mu.Unlock()
		left := jobDoneWait - time.Since(heard)
		if left <= 0 {
			return assistantwire.TranscriptionFinishResult{}, s.extTranscribeFailed(job, method, errStalled)
		}
		t := time.NewTimer(left)
		select {
		case <-job.done:
			t.Stop()
			job.mu.Lock()
			err := job.err
			out := assistantwire.TranscriptionFinishResult{Segments: slices.Clone(job.segments), Gaps: slices.Clone(job.gaps),
				Seconds: float64(job.sent) / kb.AudioRate}
			job.mu.Unlock()
			if err != nil {
				return assistantwire.TranscriptionFinishResult{}, s.extTranscribeFailed(job, method, err)
			}
			if out.Segments == nil {
				out.Segments = []assistantwire.TranscriptionSegment{}
			}
			s.endExtJob(job, "finished")
			return out, nil
		case <-job.wake: // a frame came: the wait starts over from it
		case <-t.C:
		case <-ctx.Done():
			t.Stop()
			return assistantwire.TranscriptionFinishResult{}, s.extTranscribeFailed(job, method, ctx.Err())
		}
		t.Stop()
	}
}

func (s *Service) extTranscribeCancel(_ context.Context, extension string, p assistantwire.TranscriptionJobParams) (buswire.None, error) {
	const method = assistantwire.MethodTranscriptionCancel
	s.extTr.mu.Lock()
	job := s.extTr.jobs[p.Job]
	s.extTr.mu.Unlock()
	if job == nil || job.ext != extension {
		return buswire.None{}, invalidTranscription(method, "no such job (finished, cancelled, or not this extension's)")
	}
	s.endExtJob(job, "cancelled")
	return buswire.None{}, nil
}

// endExtJob ends job (its socket closed, its place given back) and logs it once: how it ended, never what was said.
func (s *Service) endExtJob(job *extJob, how string) {
	s.extTr.mu.Lock()
	if s.extTr.jobs[job.id] == job {
		delete(s.extTr.jobs, job.id)
	}
	s.extTr.mu.Unlock()
	job.mu.Lock()
	if job.ended {
		job.mu.Unlock()
		return
	}
	job.ended = true
	if job.idle != nil {
		job.idle.Stop()
	}
	jc := job.jc
	seconds, sentences, gaps, usage := float64(job.sent)/kb.AudioRate, len(job.segments), len(job.gaps), job.usage
	job.mu.Unlock()
	job.stop()
	if jc != nil {
		jc.Close()
	}
	if jc == nil && how == "unavailable" {
		return // nothing ran: the caller has the reason
	}
	s.logf("extension %s: transcription %q %s: %.1f s of audio, %d sentence(s), %d gap(s), %.1f s billed", job.ext, job.label, how,
		seconds, sentences, gaps, usage)
}

// transcriptionLabel is a label as the log keeps it: one line, no control or format characters, cut short.
func transcriptionLabel(s string) string {
	s, _, _ = strings.Cut(s, "\n")
	s = strings.TrimSpace(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, s))
	if r := []rune(s); len(r) > extTranscribeLabelMost {
		s = string(r[:extTranscribeLabelMost]) + "…"
	}
	return s
}
