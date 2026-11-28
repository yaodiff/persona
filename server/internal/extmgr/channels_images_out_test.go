package extmgr

import (
	"context"
	"encoding/json"
	"net"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/channel"
	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/extension/host"
	"github.com/nwverse/persona/server/internal/rpc"
)

// TestChannelSendGivesPicturesOnlyToAChannelThatSendsThem: channels.send drops the pictures for a channel whose
// manifest doesn't declare images_out, and passes them to one that does (#11986).
func TestChannelSendGivesPicturesOnlyToAChannelThatSendsThem(t *testing.T) {
	root := shortTempDir(t)
	h := New(Options{Dir: root, Config: &Config{dir: root}})
	h.extensions = newExtensions(root, nil, func(string, ...any) {})
	const id = "example-chat"
	manifest := &extension.Manifest{ManifestVersion: 1, ID: id, Name: "Example chat", Version: "1.0.0",
		Contributes: map[string]json.RawMessage{"channel": json.RawMessage(`[
			{"id":"example-text","name":"Text only","kind":"im","capabilities":{"images_in":true}},
			{"id":"example-pictures","name":"Pictures","kind":"im","capabilities":{"images_in":true,"images_out":true}}]`)}}
	if err := h.extensions.reg.AddHubJSON(manifest, nil, true); err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(root, "chat.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	var mu sync.Mutex
	sent := map[string]channel.SendParams{}
	served := make(chan struct{})
	go func() {
		defer close(served)
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		peer := rpc.New("chat", conn)
		peer.OnRequest = func(_ context.Context, method string, params json.RawMessage) (any, error) {
			switch method {
			case extension.MInitialize:
				return extension.InitializeResult{Protocol: extension.Protocol, ID: id, Version: "1.0.0"}, nil
			case channel.InPlugin(channel.MSend):
				var p channel.SendParams
				if err := json.Unmarshal(params, &p); err != nil {
					return nil, err
				}
				mu.Lock()
				sent[p.Ref] = p
				mu.Unlock()
				return channel.SendResult{}, nil
			default:
				return map[string]any{}, nil
			}
		}
		_ = peer.Serve(ctx, conn)
	}()
	process := host.Start(ctx, host.Spec{ID: id, RPC: true, RPCSocket: socket, Attached: true, OnCall: true,
		Init: func(context.Context) (extension.InitializeParams, error) {
			return extension.InitializeParams{Protocol: extension.Protocol}, nil
		}})
	h.extensions.byID[id] = &hubExtension{id: id, proc: process}
	t.Cleanup(func() {
		cancel()
		process.Stop()
		_ = listener.Close()
		select {
		case <-served:
		case <-time.After(10 * time.Second):
			t.Error("channel RPC fixture did not stop")
		}
	})

	picture := []channel.Blob{{MIME: "image/png", Data: []byte("\x89PNG\r\n\x1a\n")}}
	for _, c := range []struct {
		channel string
		want    int
	}{{"example-text", 0}, {"example-pictures", 1}} {
		ref := "ref-" + c.channel
		if _, err := h.channelSendPiece(ctx, c.channel, channel.SendParams{Ref: ref, Text: "look", Images: picture}); err != nil {
			t.Fatalf("%s: send: %v", c.channel, err)
		}
		mu.Lock()
		p, ok := sent[ref]
		mu.Unlock()
		if !ok {
			t.Fatalf("%s: the extension got no channel.send", c.channel)
		}
		if p.Text != "look" || len(p.Images) != c.want {
			t.Errorf("%s: the extension got text %q and %d pictures, want %q and %d", c.channel, p.Text, len(p.Images), "look", c.want)
		}
	}
}
