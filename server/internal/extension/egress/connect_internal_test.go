package egress

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"slices"
	"testing"
	"testing/synctest"
	"time"
)

type helloReader struct {
	net.Conn
	r io.Reader
}

func (c *helloReader) Read(b []byte) (int, error) {
	// Split every TLS header and payload across TCP reads.
	return c.r.Read(b[:1])
}

func helloRecord(b []byte) []byte {
	return append([]byte{0x16, 3, 1, byte(len(b) >> 8), byte(len(b))}, b...)
}

func TestClientHelloPreservesRecordAndTCPFragments(t *testing.T) {
	handshake := []byte{1, 0, 0, 6, 3, 3, 0, 1, 2, 3}
	for split := 1; split < len(handshake); split++ {
		flight := append(helloRecord(handshake[:split]), helloRecord(handshake[split:])...)
		// TLS early application data must remain unread and never be replayed.
		early := []byte{0x17, 3, 3, 0, 1, 42}
		reader := bytes.NewReader(append(slices.Clone(flight), early...))
		got := clientHello(&helloReader{r: reader})
		if !got.tls || got.err != nil || !bytes.Equal(got.data, flight) {
			t.Fatalf("split %d: hello %+v, want %v", split, got, flight)
		}
		rest, _ := io.ReadAll(reader)
		if !bytes.Equal(rest, early) {
			t.Fatalf("split %d consumed early application data: %v", split, rest)
		}
	}
}

func TestClientHelloRejectsUnsafeReplayWithoutLosingBytes(t *testing.T) {
	for name, input := range map[string][]byte{
		"plain":         []byte("ordinary data"),
		"partial":       {0x16, 3},
		"wrong version": {0x16, 2, 0, 0, 4, 1, 0, 0, 0},
		"large record":  {0x16, 3, 3, 0xff, 0xff},
		"large hello":   helloRecord([]byte{1, 1, 0, 0}),
		"other message": helloRecord([]byte{2, 0, 0, 1, 0}),
		"extra message": helloRecord([]byte{1, 0, 0, 1, 0, 42}),
	} {
		t.Run(name, func(t *testing.T) {
			reader := bytes.NewReader(input)
			got := clientHello(&helloReader{r: reader})
			if got.tls {
				t.Fatal("unsafe input selected for replay")
			}
			rest, _ := io.ReadAll(reader)
			if !bytes.Equal(append(got.data, rest...), input) {
				t.Fatal("fallback lost client bytes")
			}
		})
	}
}

func TestConnectCacheIsBoundedAndCannotBypassFreshDNS(t *testing.T) {
	p := &Proxy{}
	for i := range connectCacheMost + 20 {
		p.rememberConnect(connectKey{host: fmt.Sprintf("service-%d.example.com", i), port: "443"}, "192.0.2.1:443")
	}
	if len(p.lastGood) != connectCacheMost {
		t.Fatalf("cache contains %d entries", len(p.lastGood))
	}
	synctest.Test(t, func(t *testing.T) {
		f := &fakeNet{t: t, plan: map[string]fakeHop{
			"192.0.2.1:443": {}, "192.0.2.2:443": {},
		}}
		p := racingProxy(f, "192.0.2.1", "192.0.2.2", "127.0.0.1")
		key := connectKey{host: "api.example.com", port: "443"}
		p.rememberConnect(key, "127.0.0.1:443")
		ctx := context.WithValue(context.Background(), connectDialKey{}, true)
		c, err := p.dialOut(ctx, "api.example.com", "443", Rule{})
		if err != nil {
			t.Fatal(err)
		}
		_ = c.Close()
		if got := f.dialed(); !slices.Equal(got, []string{"192.0.2.1:443"}) {
			t.Fatalf("cached forbidden address bypassed filtering: %v", got)
		}
		if _, err := p.dialConfigured(ctx, "api.example.com", "443"); !errors.Is(err, errNotForSettings) {
			t.Fatalf("configured name with forbidden address: %v", err)
		}
		p = racingProxy(f, "192.0.2.1", "192.0.2.2")
		p.rememberConnect(key, "192.0.2.99:443")
		c, err = p.dialOut(ctx, "api.example.com", "443", Rule{})
		if err != nil {
			t.Fatal(err)
		}
		_ = c.Close()
		if got := f.dialed(); !slices.Equal(got, []string{"192.0.2.1:443", "192.0.2.1:443"}) {
			t.Fatalf("stale cached address was dialed: %v", got)
		}
	})
}

func TestConnectCancellationClosesReplacement(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		far, silent := net.Pipe()
		defer far.Close()
		defer silent.Close()
		go func() { _, _ = io.Copy(io.Discard, silent) }()
		replacement, peer := net.Pipe()
		defer replacement.Close()
		defer peer.Close()
		hello := helloRecord([]byte{1, 0, 0, 1, 0})
		closed := make(chan error, 1)
		go func() {
			b := make([]byte, len(hello))
			_, _ = io.ReadFull(peer, b)
			cancel()
			_, err := peer.Read(b)
			closed <- err
		}()
		p := &Proxy{}
		plan := &connectPlan{
			Conn:      far,
			target:    "192.0.2.1:443",
			remaining: []string{"192.0.2.2:443"},
			attempt:   func(context.Context, string) (net.Conn, error) { return replacement, nil },
		}
		began := time.Now()
		_, c, err := p.tryConnect(ctx, nil, plan, hello, readConnectByte(far), time.Second)
		if !errors.Is(err, context.Canceled) || c != nil {
			t.Fatalf("canceled retry returned %v, %v", c, err)
		}
		if took := time.Since(began); took != time.Second {
			t.Fatalf("cancellation waited for replacement budget: %s", took)
		}
		if err := <-closed; err != io.EOF {
			t.Fatalf("replacement did not close: %v", err)
		}
	})
}
