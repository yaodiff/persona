package node

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"math"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// The pixel scroll record against the shared fixture (spec/fixtures/screen-input, also read by PersonaInputKit).
func TestInputRecordsAgainstTheFixture(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "spec", "fixtures", "screen-input", "records.json"))
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Valid []struct {
			Name, Hex             string
			Kind, Phase, Momentum uint8
			Dy, Dx                float32
		}
		Invalid []struct{ Name, Hex, Error string }
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	if len(f.Valid) < 9 || len(f.Invalid) < 6 {
		t.Fatalf("fixture too small: %d valid, %d invalid", len(f.Valid), len(f.Invalid))
	}
	for _, v := range f.Valid {
		b, _ := hex.DecodeString(v.Hex)
		r, err := decodeInputRecord(b)
		want := inputRecord{kind: v.Kind, phase: v.Phase, momentum: v.Momentum, dy: v.Dy, dx: v.Dx}
		if err != nil || r != want {
			t.Errorf("%s: %+v, %v; want %+v", v.Name, r, err, want)
		}
	}
	for _, v := range f.Invalid {
		b, _ := hex.DecodeString(v.Hex)
		if _, err := decodeInputRecord(b); err != errInputRecord(v.Error) {
			t.Errorf("%s: %v, want bad %s", v.Name, err, v.Error)
		}
	}
}

// record is a scroll record's bytes.
func record(kind, phase, momentum uint8, dy, dx float32) []byte {
	b := []byte{inputRecordType, inputRecordVersion, kind, phase, momentum, 0, 0, 0}
	b = binary.BigEndian.AppendUint32(b, math.Float32bits(dy))
	return binary.BigEndian.AppendUint32(b, math.Float32bits(dx))
}

// fakeSink stands in for the pair's Persona Input session.
type fakeSink struct {
	mu      sync.Mutex
	takes   bool
	hellos  int
	records [][]byte
	closed  int
}

func (s *fakeSink) hello() { s.mu.Lock(); s.hellos++; s.mu.Unlock() }
func (s *fakeSink) scroll(raw []byte) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.takes {
		s.records = append(s.records, append([]byte(nil), raw...))
	}
	return s.takes
}
func (s *fakeSink) close() { s.mu.Lock(); s.closed++; s.mu.Unlock() }

// screenSide is Screen Sharing for rfbInputConn: Read gives what the test says next; Write keeps what reached it.
type screenSide struct {
	mu      sync.Mutex
	next    [][]byte
	written bytes.Buffer
}

func (s *screenSide) Read(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.next) == 0 {
		return 0, nil
	}
	n := copy(p, s.next[0])
	if n < len(s.next[0]) {
		s.next[0] = s.next[0][n:]
	} else {
		s.next = s.next[1:]
	}
	return n, nil
}
func (s *screenSide) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.written.Write(p)
}
func (s *screenSide) Close() error                     { return nil }
func (s *screenSide) LocalAddr() net.Addr              { return nil }
func (s *screenSide) RemoteAddr() net.Addr             { return nil }
func (s *screenSide) SetDeadline(time.Time) error      { return nil }
func (s *screenSide) SetReadDeadline(time.Time) error  { return nil }
func (s *screenSide) SetWriteDeadline(time.Time) error { return nil }
func (s *screenSide) got() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]byte(nil), s.written.Bytes()...)
}
func (s *screenSide) say(c *rfbInputConn, b ...byte) {
	s.mu.Lock()
	s.next = append(s.next, b)
	s.mu.Unlock()
	readAll(c, len(b))
}
func readAll(c *rfbInputConn, n int) {
	buf := make([]byte, n)
	for got := 0; got < n; {
		k, _ := c.Read(buf)
		got += k
	}
}
func (s *screenSide) reset()      { s.mu.Lock(); s.written.Reset(); s.mu.Unlock() }
func repeat(b byte, n int) []byte { return bytes.Repeat([]byte{b}, n) }
func be16(v int) []byte           { return []byte{byte(v >> 8), byte(v)} }

// signIn walks c through a login of security type `typ` (30 with this key length), turn by turn as the phone and Screen
// Sharing take them, and checks every handshake byte went on.
func signIn(t *testing.T, c *rfbInputConn, s *screenSide, typ byte, keyLen int) {
	t.Helper()
	write := func(b []byte) {
		if n, err := c.Write(b); err != nil || n != len(b) {
			t.Fatalf("write: %d, %v", n, err)
		}
	}
	var phone []byte
	s.say(c, []byte("RFB 003.889\n")...)
	write([]byte("RFB 003.889\n"))
	phone = append(phone, "RFB 003.889\n"...)
	s.say(c, 4, 30, 33, 35, 36)
	write([]byte{typ})
	phone = append(phone, typ)
	switch typ {
	case rfbSecAppleDH:
		s.say(c, append(append([]byte{0, 2}, be16(keyLen)...), repeat(7, 2*keyLen)...)...)
		auth := repeat(9, 128+keyLen)
		write(auth[:50]) // the answer in pieces too
		write(auth[50:])
		phone = append(phone, auth...)
	case rfbSecVNCAuth:
		s.say(c, repeat(3, 16)...)
		write(repeat(4, 16))
		phone = append(phone, repeat(4, 16)...)
	}
	s.say(c, 0, 0, 0, 0) // SecurityResult: OK
	write([]byte{1})     // ClientInit
	phone = append(phone, 1)
	if got := s.got(); !bytes.Equal(got, phone) {
		t.Fatalf("the handshake went on as %x, want %x", got, phone)
	}
	s.reset()
}

// messages is every client message the phone sends, records among them: what Screen Sharing must get (the records
// taken out) and the records in order.
func messages() (stream, want []byte, records [][]byte) {
	add := func(b []byte, isRecord bool) {
		stream = append(stream, b...)
		if isRecord {
			records = append(records, b)
		} else {
			want = append(want, b...)
		}
	}
	add(append([]byte{0}, repeat(1, 19)...), false)                                 // SetPixelFormat
	add([]byte{2, 0, 0, 3, 0, 0, 0, 16, 0, 0, 0, 1, 0xff, 0xff, 0xff, 0x21}, false) // SetEncodings, 3
	add([]byte{3, 1, 0, 0, 0, 0, 7, 128, 4, 56}, false)                             // FramebufferUpdateRequest
	add(record(inputKindHello, 0, 0, 0, 0), true)
	add([]byte{5, 0, 1, 44, 1, 144}, false) // PointerEvent at 300, 400
	add(record(inputKindScroll, 1, 0, -12.5, 0), true)
	add(record(inputKindScroll, 2, 0, -3, 0.5), true)
	add([]byte{4, 1, 0, 0, 0, 0, 0xff, 0x0d}, false)                    // KeyEvent
	add([]byte{6, 0, 0, 0, 0, 0, 0, 5, 'h', 'e', 'l', 'l', 'o'}, false) // ClientCutText
	add(record(inputKindScroll, 4, 0, 0, 0), true)
	add(record(inputKindScroll, 0, 1, -40, 0), true)
	add([]byte{5, 1, 0, 10, 0, 20}, false)
	return
}

// After each login the node follows, every client message goes on as it came, byte for byte, and the records come
// out to the helper in order, however the stream is cut.
func TestRecordsComeOutOfTheStreamAndEverythingElseGoesOn(t *testing.T) {
	stream, want, records := messages()
	for _, login := range []struct {
		name   string
		typ    byte
		keyLen int
	}{{"Apple Diffie-Hellman, 128-byte key", rfbSecAppleDH, 128}, {"Apple Diffie-Hellman, 256-byte key", rfbSecAppleDH, 256},
		{"VNC password", rfbSecVNCAuth, 0}, {"none", rfbSecNone, 0}} {
		for _, cut := range []int{1, 2, 3, 5, 7, 16, 17, 31, len(stream)} {
			s, sink := &screenSide{}, &fakeSink{takes: true}
			c := newRFBInputConn(s, sink)
			signIn(t, c, s, login.typ, login.keyLen)
			for at := 0; at < len(stream); at += cut {
				part := stream[at:min(at+cut, len(stream))]
				if n, err := c.Write(part); err != nil || n != len(part) {
					t.Fatalf("%s, pieces of %d: write %d, %v", login.name, cut, n, err)
				}
			}
			if got := s.got(); !bytes.Equal(got, want) {
				t.Errorf("%s, pieces of %d: Screen Sharing got\n%x\nwant\n%x", login.name, cut, got, want)
			}
			if sink.hellos != 1 || len(sink.records) != len(records)-1 {
				t.Errorf("%s, pieces of %d: %d hellos and %d records, want 1 and %d", login.name, cut, sink.hellos, len(sink.records), len(records)-1)
				continue
			}
			for i, r := range sink.records {
				if !bytes.Equal(r, records[i+1]) {
					t.Errorf("%s, pieces of %d: record %d is %x, want %x", login.name, cut, i, r, records[i+1])
				}
			}
		}
	}
}

// A security type the node doesn't follow (its session may not be plain RFB), or a message it doesn't know, and nothing
// more is read: the rest goes on as it came, records and all.
func TestAnythingUnknownAndTheStreamPassesAsItCame(t *testing.T) {
	stream, _, _ := messages()

	s, sink := &screenSide{}, &fakeSink{takes: true}
	c := newRFBInputConn(s, sink)
	s.say(c, []byte("RFB 003.889\n")...)
	_, _ = c.Write([]byte("RFB 003.889\n"))
	s.say(c, 2, 30, 36)
	_, _ = c.Write([]byte{36}) // another of Apple's types
	s.reset()
	_, _ = c.Write(stream)
	if got := s.got(); !bytes.Equal(got, stream) || sink.hellos != 0 || len(sink.records) != 0 {
		t.Errorf("type 36: went on as %d bytes (want all %d), %d hellos, %d records", len(got), len(stream), sink.hellos, len(sink.records))
	}

	s, sink = &screenSide{}, &fakeSink{takes: true}
	c = newRFBInputConn(s, sink)
	signIn(t, c, s, rfbSecAppleDH, 128)
	continuous := []byte{150, 1, 0, 0, 0, 0, 7, 128, 4, 56} // EnableContinuousUpdates: not one of the six
	_, _ = c.Write(append(append([]byte(nil), continuous...), stream...))
	if got := s.got(); !bytes.Equal(got, append(continuous, stream...)) || len(sink.records) != 0 {
		t.Errorf("after an unknown message: went on as %d bytes, %d records", len(got), len(sink.records))
	}
}

// A scroll the helper can't take goes to Screen Sharing as wheel notches, a notch a Mac point, at the pointer the phone
// last placed and with its buttons: fractions carry to the next record, a new gesture starts clean, and no record turns
// into more than inputFallbackMost.
func TestAScrollTheHelperCantTakeBecomesNotches(t *testing.T) {
	s, sink := &screenSide{}, &fakeSink{takes: false}
	c := newRFBInputConn(s, sink)
	signIn(t, c, s, rfbSecAppleDH, 128)
	pointer := []byte{5, 1, 1, 44, 1, 144} // the left button held, at 300, 400
	_, _ = c.Write(pointer)
	s.reset()
	notch := func(n int, bit byte) []byte {
		var b []byte
		for range n {
			b = append(b, 5, 1|bit, 1, 44, 1, 144, 5, 1, 1, 44, 1, 144)
		}
		return b
	}
	steps := []struct {
		record []byte
		want   []byte
	}{
		{record(inputKindScroll, 1, 0, 2.5, 0), notch(2, 8)},   // 2, .5 left
		{record(inputKindScroll, 2, 0, 2.5, 0), notch(3, 8)},   // 3 with the half
		{record(inputKindScroll, 2, 0, 0.75, 0), nil},          // under one
		{record(inputKindScroll, 1, 0, -1.5, 0), notch(1, 16)}, // a new gesture: the .75 is gone
		{record(inputKindScroll, 0, 2, -1e6, 0), notch(inputFallbackMost, 16)},
		{[]byte{inputRecordType, 2, 1, 2, 0, 0, 0, 0, 0x41, 0, 0, 0, 0, 0, 0, 0}, nil}, // a newer version: dropped, never passed on
	}
	for i, step := range steps {
		_, _ = c.Write(step.record)
		if got := s.got(); !bytes.Equal(got, step.want) {
			t.Errorf("step %d: Screen Sharing got %d bytes, want %d", i, len(got), len(step.want))
		}
		s.reset()
	}
	if err := c.Close(); err != nil || sink.closed != 1 {
		t.Errorf("close: %v, the session closed %d times", err, sink.closed)
	}
	_ = c.Close()
	if sink.closed != 1 {
		t.Errorf("closed twice: the session closed %d times", sink.closed)
	}
}
