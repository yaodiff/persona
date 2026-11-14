package hub

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/nwverse/persona/server/internal/rpc"
)

// screenVideoConnect is GET …/screen/video/connect?pair=…: a binary WebSocket carrying the live-screen hardware video
// channel (#3954). It is the same gate as …/ports/5900/connect (a full device, cua or ego on for the Mac) and uses
// the same per-machine stream budget. It calls screen.dial instead of port.dial; the node binds the stream to the
// signed-in RFB stream carrying the same `pair`. The hub never parses the bytes.
func (h *Hub) screenVideoConnect(w http.ResponseWriter, r *http.Request, m *Machine) {
	if principalOf(r.Context()).Role != roleFull {
		writeErr(w, http.StatusForbidden, errors.New("this device cannot open the screen"))
		return
	}
	if !h.screenAllowed(m) {
		writeErr(w, http.StatusForbidden, errors.New("this machine's screen is not shared"))
		return
	}
	pair := r.URL.Query().Get("pair")
	if pair == "" {
		writeErr(w, http.StatusBadRequest, errors.New("a pair is required"))
		return
	}
	mux := m.portStreams()
	if mux == nil {
		writeErr(w, http.StatusServiceUnavailable, fmt.Errorf("%s: %w", m.name(), errOffline))
		return
	}
	if mux.Len() >= maxPortStreams {
		writeErr(w, http.StatusTooManyRequests, errors.New("too many connections to this machine"))
		return
	}
	r, untrack := h.trackRelayed(r)
	defer untrack()
	if err := r.Context().Err(); err != nil {
		callErr(w, err)
		return
	}
	id := hubStreamIDs.next("v")
	if err := mux.Open(id); err != nil {
		writeErr(w, http.StatusServiceUnavailable, err)
		return
	}
	dctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	dial := map[string]any{"id": id, "pair": pair}
	if h.extReport().ScreenKeepAwake {
		dial["keep_awake"] = true // someone is watching: the Mac stays awake until this closes (node/awake.go)
	}
	link := h.askScreenLink(r, dial) // the video joins the pair's screen session (#6398)
	var res struct {
		Link int `json:"link"`
	}
	err := m.call(dctx, "screen.dial", dial, &res)
	cancel()
	if err != nil {
		mux.Abort(id, err)
		var re *rpc.Error
		if errors.As(err, &re) {
			switch re.Code {
			case "not_signed_in":
				writeErr(w, http.StatusForbidden, err) // the phone falls back to RFB
				return
			case "busy":
				writeErr(w, http.StatusTooManyRequests, err)
				return
			case "video_unavailable":
				writeErr(w, http.StatusServiceUnavailable, err)
				return
			case "session_gone":
				writeErr(w, http.StatusGone, err)
				return
			}
		}
		callErr(w, err)
		return
	}
	h.bridgeStream(w, r, mux, id, link && res.Link == 1)
}
