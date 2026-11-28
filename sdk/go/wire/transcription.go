package wire

// The transcription extension point's job wire, for a program that serves one (docs/assistant/meeting-minutes.md
// "The `transcription` extension point"). Hand-written, not generated from schema/extension: the hub's own code
// (server/internal/extwire) refers to these names, and a program built outside personad uses them the same way.

// TranscriptionProtocol is the transcription point's job wire (meeting-minutes.md "The `transcription` extension
// point"): GET /transcription/{cid}/info and the job socket GET /transcription/{cid}/ws on the extension's own socket.
const TranscriptionProtocol = 1

// Features of a contribution (/info) and of one job (ready).
const (
	TranscriptionLive    = "live"
	TranscriptionReplay  = "replay"
	TranscriptionInterim = "interim"
)

// Frame types. Manager → extension: start, seek, flush, reference, end (and binary PCM16 LE mono 16 kHz).
// Extension → manager: ready, interim, sentence, ack, flushed (every sentence before a flush's at was sent), window
// (replay: every sentence of a window was sent), usage (the upstream's billed seconds), warning, error, done.
const (
	TFStart, TFSeek, TFFlush, TFReference, TFEnd = "start", "seek", "flush", "reference", "end"
	TFReady, TFInterim, TFSentence, TFAck        = "ready", "interim", "sentence", "ack"
	TFFlushed, TFWindow, TFUsage                 = "flushed", "window", "usage"
	TFWarning, TFError, TFDone                   = "warning", "error", "done"
)

// Codes (open sets).
const (
	TWSessionDropped, TWNoInterim, TWBehind = "session_dropped", "no_interim", "behind"
	TENoUpstream, TEUpstreamForbidden       = "no_upstream", "upstream_forbidden"
	TEUpstreamUnavailable, TEUpstreamFailed = "upstream_unavailable", "upstream_failed"
	TEUpstreamBusy                          = "upstream_busy" // busy or rate-limited: too many sessions, quota used up (#13446)
	TEBadFrame                              = "bad_frame"
	TSetupLiveUpstream                      = "live_upstream" // /info.setup: no GPT-Live (live dialect) upstream
)

// TranscriptionInfo is GET /transcription/{cid}/info.
type TranscriptionInfo struct {
	Protocol int      `json:"protocol"`
	Features []string `json:"features"`
	Setup    string   `json:"setup,omitempty"`
}

// TranscriptionFrame is any text frame of a job, both ways; Type says which fields it has. Times are recording
// seconds (never wall time, also at speed 4).
type TranscriptionFrame struct {
	Type     string   `json:"type"`
	Mode     string   `json:"mode,omitempty"`     // start: live | replay
	Speed    float64  `json:"speed,omitempty"`    // start, replay: 4
	Language string   `json:"language,omitempty"` // start: a hint, never a lock
	Glossary []string `json:"glossary,omitempty"` // start: spellings, data only
	At       *float64 `json:"at,omitempty"`       // start, seek, ack, flushed
	From     *float64 `json:"from,omitempty"`     // reference, window
	To       *float64 `json:"to,omitempty"`       // reference, window
	Start    *float64 `json:"start,omitempty"`    // interim, sentence
	End      *float64 `json:"end,omitempty"`      // sentence
	Text     *string  `json:"text,omitempty"`     // reference, interim, sentence
	Features []string `json:"features,omitempty"` // ready
	Kept     bool     `json:"kept,omitempty"`     // window
	Seconds  float64  `json:"seconds,omitempty"`  // usage
	Sessions int      `json:"sessions,omitempty"` // usage
	Code     string   `json:"code,omitempty"`     // warning, error
	Message  string   `json:"message,omitempty"`  // warning, error (never meeting text)
	Fatal    bool     `json:"fatal,omitempty"`    // error
}

// F is a pointer to v for the frames' optional numbers and texts.
func F[T any](v T) *T { return &v }

// Headers the hub sets on a request it relays to an extension's program: the requester's role and the language the
// hub would answer it in (the hub's full list is in server/internal/extwire).
const (
	HeaderRole = "X-Persona-Role" // full | answer | extension | …
	HeaderLang = "X-Persona-Lang"
)
