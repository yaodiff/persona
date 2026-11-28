package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"path"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/compat"
	"github.com/nwverse/persona/server/internal/proto"
)

// spaceMDNode is a fake node's SPACE.md of track qa-plain: what it holds, the calls it was made, and whether its
// computer is away.
type spaceMDNode struct {
	content string
	calls   []assistantwire.SpaceMDCall
	offline bool
}

// spaceMDService is spaceTrackService with nodes that keep SPACE.md, a conversation in qa-plain (plain-00-0000), and
// a node answering assistant.space_md the way node/assistant_space_md.go does, its digest the content's length.
func spaceMDService(t *testing.T) (*Service, *fakeHub, *spaceMDNode) {
	t.Helper()
	s, f := spaceTrackService(t)
	f.sessions["server"] = append(f.sessions["server"], proto.Session{SessionID: "plain-00-0000", Cwd: "/home/me/.persona/tracks/qa-plain",
		Assistant: true, Space: "qa-plain", Title: "QA plain", LastText: "budget"})
	// With devices the manager answers session.get from its own nodes' lists, never the fake hub's: the server's node
	// lists the same conversations, so selfSpace finds plain-00-0000 in qa-plain.
	fd := newFleetDevs()
	fd.add("server", "server").home = "/home/me"
	fd.setSessions("server", append([]proto.Session(nil), f.sessions["server"]...))
	s.devs = spaceDevs{fleetDevs: fd}
	n := &spaceMDNode{content: "# QA plain\n\n" + spaceStateBegin + "\n" + spaceStateEnd + "\n"}
	digest := func(s string) string { return fmt.Sprint(len(s)) }
	f.answerNode(assistantwire.DeviceAssistantSpaceMD, func(_ string, raw json.RawMessage) (any, error) {
		var c assistantwire.SpaceMDCall
		if err := json.Unmarshal(raw, &c); err != nil {
			return nil, err
		}
		n.calls = append(n.calls, c)
		if n.offline {
			return nil, fmt.Errorf("server: %w", apierr.ErrOffline)
		}
		cur := assistantwire.SpaceMD{Content: n.content, Digest: digest(n.content)}
		switch c.Op {
		case assistantwire.SpaceMDPutHuman:
			switch {
			case c.BaseDigest != cur.Digest:
				cur.Refused = assistantwire.SpaceMDStale
			case !strings.Contains(c.Content, spaceStateBegin+"\n"+spaceStateEnd):
				cur.Refused = assistantwire.SpaceMDManaged
			default:
				n.content = c.Content
				cur = assistantwire.SpaceMD{Content: n.content, Digest: digest(n.content)}
			}
		case assistantwire.SpaceMDPutState:
			cur.State = c.State
		}
		return cur, nil
	})
	return s, f, n
}

// The SPACE.md routes (#13472): reads for full-access clients, writes for the user only, each on the assistant's
// machine in the track's own directory; a refusal of the node's is the route's status, and a write prepares the track
// again so its instructions end with the new SPACE.md.
func TestTrackSpaceMDRoutes(t *testing.T) {
	s, f, n := spaceMDService(t)
	trackDir := path.Join(compat.StateDir, "tracks/qa-plain")

	w := serve(s, http.MethodGet, "/api/v1/assistant/tracks/qa-plain/space-md", "")
	got := decodeTrackJSON[assistantwire.SpaceMD](t, w.Body.Bytes())
	if w.Code != http.StatusOK || got.Content != n.content || got.Digest == "" || strings.Contains(w.Body.String(), "refused") {
		t.Fatalf("get: %d %s", w.Code, w.Body)
	}
	if c := n.calls[0]; c.Op != assistantwire.SpaceMDGet || c.Dir != trackDir || c.Slug != "qa-plain" {
		t.Errorf("node call: %+v", c)
	}
	for _, slug := range []string{"qa-unknown", "qa-elsewhere", "qa-gone", "qa-health-like", SpaceHealth} {
		if w := serve(s, http.MethodGet, "/api/v1/assistant/tracks/"+slug+"/space-md", ""); w.Code != http.StatusNotFound {
			t.Errorf("%s: %d, want 404", slug, w.Code)
		}
	}

	calls := len(n.calls)
	body := func(content, base string) string {
		b, _ := json.Marshal(assistantwire.SpaceMDPut{Content: content, BaseDigest: base})
		return string(b)
	}
	edited := strings.Replace(n.content, "# QA plain\n", "# QA plain\n\nAnswer with sources.\n", 1)
	for _, role := range []string{roleExtension, "scoped"} {
		if w := serveAs(s, role, http.MethodPut, "/api/v1/assistant/tracks/qa-plain/space-md", body(edited, got.Digest)); w.Code != http.StatusForbidden {
			t.Errorf("%s: %d, want 403", role, w.Code)
		}
	}
	for name, b := range map[string]string{
		"no base digest": body(edited, ""),
		"unknown field":  `{"content":"x","base_digest":"1","extra":true}`,
		"not JSON":       `{`,
	} {
		if w := serve(s, http.MethodPut, "/api/v1/assistant/tracks/qa-plain/space-md", b); w.Code != http.StatusBadRequest {
			t.Errorf("%s: %d, want 400", name, w.Code)
		}
	}
	if len(n.calls) != calls {
		t.Fatalf("refused writes reached the node: %+v", n.calls[calls:])
	}
	if w := serve(s, http.MethodPut, "/api/v1/assistant/tracks/qa-plain/space-md", body(edited, "stale")); w.Code != http.StatusConflict {
		t.Errorf("stale: %d, want 409", w.Code)
	}
	if w := serve(s, http.MethodPut, "/api/v1/assistant/tracks/qa-plain/space-md", body("# Mine\n", got.Digest)); w.Code != http.StatusBadRequest {
		t.Errorf("a managed block dropped: %d, want 400", w.Code)
	}
	if len(f.prepared) != 0 {
		t.Fatalf("a refused write prepared the track: %d", len(f.prepared))
	}

	w = serve(s, http.MethodPut, "/api/v1/assistant/tracks/qa-plain/space-md", body(edited, got.Digest))
	put := decodeTrackJSON[assistantwire.SpaceMD](t, w.Body.Bytes())
	if w.Code != http.StatusOK || put.Content != edited || n.content != edited {
		t.Fatalf("put: %d %s", w.Code, w.Body)
	}
	if c := n.calls[len(n.calls)-1]; c.Op != assistantwire.SpaceMDPutHuman || c.Dir != trackDir || c.BaseDigest != got.Digest {
		t.Errorf("node call: %+v", c)
	}
	if len(f.prepared) != 1 || f.prepared[0]["dir"] != trackDir {
		t.Errorf("after the write: prepared %v, want the track", f.prepared)
	}

	// The computer away, or its node without the method: 503, nothing asked of an older node.
	n.offline = true
	if w := serve(s, http.MethodGet, "/api/v1/assistant/tracks/qa-plain/space-md", ""); w.Code != http.StatusServiceUnavailable {
		t.Errorf("offline: %d, want 503", w.Code)
	}
	n.offline = false
	s.devs = spaceDevs{fleetDevs: newFleetDevs(), noSpaceMD: true}
	calls = len(n.calls)
	if w := serve(s, http.MethodGet, "/api/v1/assistant/tracks/qa-plain/space-md", ""); w.Code != http.StatusServiceUnavailable || len(n.calls) != calls {
		t.Errorf("an older node: %d, %d calls", w.Code, len(n.calls)-calls)
	}
}

// space_state_update (#13472) is offered to a space track's conversations only, routed to its handler, which refuses
// every other caller and an oversized text before the node is asked.
func TestSpaceStateUpdateTool(t *testing.T) {
	ctx := context.Background()
	s, _, n := spaceMDService(t)
	if toolListed(s.Tools(ctx), spaceStateTool) {
		t.Fatal("space_state_update is in the assistant's shared list")
	}
	if !toolListed(s.ToolsFor(ctx, "server", "plain-00-0000"), spaceStateTool) ||
		!toolListed(s.sessionMCPTools(ctx, "server", "plain-00-0000", true, false), spaceStateTool) {
		t.Fatal("the space's conversation lacks space_state_update")
	}
	for _, sid := range []string{"main-0000-0000", "sleep-000-0000"} {
		if toolListed(s.sessionMCPTools(ctx, "server", sid, true, false), spaceStateTool) {
			t.Errorf("%s is offered space_state_update", sid)
		}
	}

	res := s.mcpCall(ctx, "server", "plain-00-0000", spaceStateTool, json.RawMessage(`{"text":"Budget drafted.\n"}`), true)
	if res.IsError || len(n.calls) != 1 {
		t.Fatalf("from the space: %+v, %d calls", res, len(n.calls))
	}
	if c := n.calls[0]; c.Op != assistantwire.SpaceMDPutState || c.State != "Budget drafted." || c.Slug != "qa-plain" ||
		c.Dir != path.Join(compat.StateDir, "tracks/qa-plain") {
		t.Errorf("node call: %+v", c)
	}

	for _, tc := range []struct{ sid, args string }{
		{"main-0000-0000", `{"text":"x"}`},
		{"sleep-000-0000", `{"text":"x"}`},
		{"plain-00-0000", `{"text":"` + strings.Repeat("x", assistantwire.SpaceMDStateMax+1) + `"}`},
		{"plain-00-0000", `{"text":"x","extra":1}`},
		{"plain-00-0000", `{"state":"x"}`},
	} {
		if out, isErr := spaceTool(t, s, tc.sid, spaceStateTool, tc.args); !isErr {
			t.Errorf("%s %.40s: not refused: %s", tc.sid, tc.args, out)
		}
	}
	if res := s.mcpCall(ctx, "server", "main-0000-0000", spaceStateTool, json.RawMessage(`{"text":"x"}`), true); !res.IsError {
		t.Error("the main chat's call: not refused")
	}
	if len(n.calls) != 1 {
		t.Errorf("refused calls reached the node: %+v", n.calls[1:])
	}
}

// A conversation the space-conversations extension opened in one of its spaces writes that space's state block
// (#17556): the hub notes it as opened by an extension, which made it an outward reader, and space_state_update refused
// it as "not inside a space" although its context said it was. Only the extension contributing the space's kind is
// the space's own; one any other extension opened, or one that talks with other people, is still refused, and so is
// one whose origin cannot be told.
func TestSpaceStateUpdateFromTheSpaceKindsOwnConversation(t *testing.T) {
	ctx := context.Background()
	s, _, n := spaceMDService(t)
	const sid = "plain-00-0000"
	call := func() (string, bool) {
		return spaceTool(t, s, sid, spaceStateTool, `{"text":"Migrated: notes and decisions."}`)
	}

	// As the hub notes it (start.go requestOrigin): origin extension, ref and opener the extension's id.
	s.originSet(ctx, "server", sid, OriginExtension, testSpaceKindOwner, testSpaceKindOwner)
	if !s.outwardReader(ctx, "server", sid) {
		t.Fatal("the fixture's conversation is not an outward reader: the test no longer reproduces the refusal")
	}
	if out, isErr := call(); isErr || len(n.calls) != 1 {
		t.Fatalf("the space's own conversation: %s, %d node calls", out, len(n.calls))
	}
	if c := n.calls[0]; c.Op != assistantwire.SpaceMDPutState || c.State != "Migrated: notes and decisions." || c.Slug != "qa-plain" {
		t.Errorf("node call: %+v", c)
	}

	for _, tc := range []struct{ name, machine, origin, ext string }{
		{"another extension", "server", OriginExtension, "other-ext"},
		{"a Feishu /agent", "server", OriginLarkAgent, testSpaceKindOwner},
		{"the inbox's twin", "server", OriginInbox, ""},
		{"another machine", "elsewhere", OriginExtension, testSpaceKindOwner},
	} {
		s.originSet(ctx, tc.machine, sid, tc.origin, tc.ext, tc.ext)
		calls := len(n.calls)
		if out, isErr := call(); !isErr || len(n.calls) != calls {
			t.Errorf("%s: not refused: %s", tc.name, out)
		} else if strings.Contains(out, "inside a space") {
			t.Errorf("%s: refused as outside the space: %s", tc.name, out)
		}
	}

	// A kind the hub does not say the owner of (an older hub) opens nothing up.
	s.originSet(ctx, "server", sid, OriginExtension, testSpaceKindOwner, testSpaceKindOwner)
	s.trackKindsExt = ownerlessTrackKinds{}
	s.tracksChanged()
	calls := len(n.calls)
	if out, isErr := call(); !isErr || len(n.calls) != calls {
		t.Errorf("an ownerless kind: not refused: %s", out)
	}

	// The user's own conversation in the space was never an outward reader and still writes.
	installTestTrackKinds(s)
	s.tracksChanged()
	s.originSet(ctx, "server", sid, OriginUser, "", "")
	if out, isErr := call(); isErr || len(n.calls) != calls+1 {
		t.Errorf("the user's conversation: %s", out)
	}
}

// ownerlessTrackKinds is testTrackKinds as a hub that does not name the contributing extension gives it.
type ownerlessTrackKinds struct{}

func (ownerlessTrackKinds) DomainTrackKindsJSON(ctx context.Context) (json.RawMessage, error) {
	raw, err := testTrackKinds{}.DomainTrackKindsJSON(ctx)
	if err != nil {
		return nil, err
	}
	var items []map[string]any
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, err
	}
	for _, it := range items {
		delete(it, "extension")
	}
	return json.Marshal(items)
}
