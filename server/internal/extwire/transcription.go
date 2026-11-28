package extwire

import "github.com/nwverse/persona/sdk/go/wire"

// The transcription point's job wire lives in the Go SDK (sdk/go/wire/transcription.go) so a program outside personad
// speaks it too (#16263); these names stay for the hub's own code.

// TranscriptionProtocol is the transcription point's job wire (meeting-minutes.md "The `transcription` extension
// point"): GET /transcription/{cid}/info and the job socket GET /transcription/{cid}/ws on the extension's own socket.
const TranscriptionProtocol = wire.TranscriptionProtocol

// Features of a contribution (/info) and of one job (ready).
const (
	TranscriptionLive    = wire.TranscriptionLive
	TranscriptionReplay  = wire.TranscriptionReplay
	TranscriptionInterim = wire.TranscriptionInterim
)

// Frame types (wire.TFStart …).
const (
	TFStart, TFSeek, TFFlush, TFReference, TFEnd = wire.TFStart, wire.TFSeek, wire.TFFlush, wire.TFReference, wire.TFEnd
	TFReady, TFInterim, TFSentence, TFAck        = wire.TFReady, wire.TFInterim, wire.TFSentence, wire.TFAck
	TFFlushed, TFWindow, TFUsage                 = wire.TFFlushed, wire.TFWindow, wire.TFUsage
	TFWarning, TFError, TFDone                   = wire.TFWarning, wire.TFError, wire.TFDone
)

// Codes (open sets).
const (
	TWSessionDropped, TWNoInterim, TWBehind = wire.TWSessionDropped, wire.TWNoInterim, wire.TWBehind
	TENoUpstream, TEUpstreamForbidden       = wire.TENoUpstream, wire.TEUpstreamForbidden
	TEUpstreamUnavailable, TEUpstreamFailed = wire.TEUpstreamUnavailable, wire.TEUpstreamFailed
	TEUpstreamBusy                          = wire.TEUpstreamBusy
	TEBadFrame                              = wire.TEBadFrame
	TSetupLiveUpstream                      = wire.TSetupLiveUpstream
)

// TranscriptionInfo is GET /transcription/{cid}/info.
type TranscriptionInfo = wire.TranscriptionInfo

// TranscriptionFrame is any text frame of a job, both ways (wire.TranscriptionFrame).
type TranscriptionFrame = wire.TranscriptionFrame

// F is a pointer to v for the frames' optional numbers and texts.
func F[T any](v T) *T { return wire.F(v) }
