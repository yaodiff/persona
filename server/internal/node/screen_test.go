package node

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/rpc"
)

func dialErrCode(t *testing.T, err error) string {
	t.Helper()
	var re *rpc.Error
	if !errors.As(err, &re) {
		t.Fatalf("want *rpc.Error, got %T: %v", err, err)
	}
	return re.Code
}

func dial(t *testing.T, n *Node, pair string) (any, error) {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"id": "v1", "pair": pair})
	return n.screenDial(context.Background(), body)
}

func TestScreenDialErrorTaxonomy(t *testing.T) {
	t.Run("no helper is video_unavailable", func(t *testing.T) {
		n := &Node{screens: newScreenLogins()} // screenStart nil
		n.screens.byID["K"] = &screenPair{result: rfbLoginSignedIn, open: true}
		_, err := dial(t, n, "K")
		if code := dialErrCode(t, err); code != "video_unavailable" {
			t.Fatalf("code = %q", code)
		}
	})

	t.Run("unsigned pair is not_signed_in", func(t *testing.T) {
		n := &Node{screens: newScreenLogins()}
		n.screenStart = func(context.Context, string, string, bool, bool, func()) (bool, error) { return false, nil }
		// pair K present but login not accepted
		n.screens.byID["K"] = &screenPair{result: rfbLoginFailed, open: true}
		_, err := dial(t, n, "K")
		if code := dialErrCode(t, err); code != "not_signed_in" {
			t.Fatalf("code = %q", code)
		}
		// a pair with no RFB stream at all is also not_signed_in
		if _, err := dial(t, n, "absent"); dialErrCode(t, err) != "not_signed_in" {
			t.Fatalf("absent pair should be not_signed_in")
		}
	})

	t.Run("at the cap it is busy", func(t *testing.T) {
		n := &Node{screens: newScreenLogins()}
		n.screenStart = func(context.Context, string, string, bool, bool, func()) (bool, error) { return false, nil }
		n.screens.byID["K"] = &screenPair{result: rfbLoginSignedIn, open: true}
		n.screens.count = maxScreenStreams
		_, err := dial(t, n, "K")
		if code := dialErrCode(t, err); code != "busy" {
			t.Fatalf("code = %q", code)
		}
	})

	t.Run("signed in and free starts the helper", func(t *testing.T) {
		n := &Node{screens: newScreenLogins()}
		var gotID, gotPair string
		n.screenStart = func(_ context.Context, id, pair string, _, _ bool, _ func()) (bool, error) {
			gotID, gotPair = id, pair
			return false, nil
		}
		n.screens.byID["K"] = &screenPair{result: rfbLoginSignedIn, open: true}
		if _, err := dial(t, n, "K"); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if gotID != "v1" || gotPair != "K" {
			t.Fatalf("screenStart got id=%q pair=%q", gotID, gotPair)
		}
		if n.screens.count != 1 {
			t.Fatalf("busy count = %d, want 1", n.screens.count)
		}
	})

	t.Run("a failed start releases the slot", func(t *testing.T) {
		n := &Node{screens: newScreenLogins()}
		n.screenStart = func(context.Context, string, string, bool, bool, func()) (bool, error) {
			return false, &rpc.Error{Code: "video_unavailable", Message: "no permission"}
		}
		n.screens.byID["K"] = &screenPair{result: rfbLoginSignedIn, open: true}
		if _, err := dial(t, n, "K"); dialErrCode(t, err) != "video_unavailable" {
			t.Fatalf("want video_unavailable")
		}
		if n.screens.count != 0 {
			t.Fatalf("slot not released: count = %d", n.screens.count)
		}
	})
}

// scriptConn is a net.Conn whose reads replay a transcript (Screen Sharing → phone) and whose writes are captured
// (phone → Screen Sharing). Only Read/Write/Close are used by the tee.
type scriptConn struct {
	net.Conn
	r      *bytes.Reader
	w      bytes.Buffer
	closed bool
}

func (s *scriptConn) Read(p []byte) (int, error)  { return s.r.Read(p) }
func (s *scriptConn) Write(p []byte) (int, error) { return s.w.Write(p) }
func (s *scriptConn) Close() error                { s.closed = true; return nil }

func TestScreenLoginObserveTee(t *testing.T) {
	logins := newScreenLogins()
	serverBytes := bytesJoin(rfbVersion, []byte{1}, []byte{rfbSecNone}, u32(0))
	clientBytes := bytesJoin(rfbVersion, []byte{rfbSecNone})
	sc := &scriptConn{r: bytes.NewReader(serverBytes)}
	wrapped := logins.observe("K", sc)

	// Drain the server direction (tees into the observer), then feed the client direction.
	_, _ = io.Copy(io.Discard, wrapped)
	if _, err := wrapped.Write(clientBytes); err != nil {
		t.Fatalf("write: %v", err)
	}

	waitWithin(t, 2*time.Second, "pair to become signed-in", func() bool { return logins.signedIn("K") })

	wrapped.Close()
	if !sc.closed {
		t.Fatalf("underlying conn not closed")
	}
	if logins.signedIn("K") {
		t.Fatalf("pair should not be signed-in after the RFB stream closed")
	}
}
