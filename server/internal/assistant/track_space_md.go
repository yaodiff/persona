package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/nodeproto"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// A track's SPACE.md, read and written on the assistant's machine (#13472, docs/assistant/health.md "Tracks and
// kinds"): the apps read it and the user writes the part that is theirs (GET / PUT
// /api/v1/assistant/tracks/{slug}/space-md); the track's own conversations replace the state block Persona keeps
// (space_state_update, offered to them only). The node does both (assistant.space_md, nodeproto.TrackSpaceMD): it
// keeps every managed block's bytes on the user's write and every other byte on the state's, and backs the file up
// first.

// spaceStateTool is the MCP tool a space track's conversations replace their SPACE.md's state block with.
const spaceStateTool = "space_state_update"

// spaceMDBodyMax bounds a PUT's body: a 64 KB SPACE.md, escaped.
const spaceMDBodyMax = 512 << 10

// errSpaceMDNode: the call did not reach a node that keeps SPACE.md (offline, or not announcing it).
var errSpaceMDNode = errors.New("the assistant's computer cannot be reached for SPACE.md")

func (s *Service) trackSpaceMDRoutes() {
	s.handle("GET /api/v1/assistant/tracks/{slug}/space-md", s.trackSpaceMDGet)
	s.handle("PUT /api/v1/assistant/tracks/{slug}/space-md", s.trackSpaceMDPut)
}

// trackSpaceMDNode: machine's node reads and writes a track's SPACE.md (nodeproto.TrackSpaceMD).
func (s *Service) trackSpaceMDNode(machine string) bool {
	nodes, ok := s.physicalDevices().(nodeAnnouncer)
	return ok && nodes.NodeAnnounced(machine, nodeproto.TrackSpaceMD)
}

// spaceMD makes call c on the SPACE.md of space track slug, on the assistant's machine. The status says what went
// wrong when err is set: 404 no such track (or no directory of its own), 503 the machine's node cannot be asked.
func (s *Service) spaceMD(ctx context.Context, slug string, c assistantwire.SpaceMDCall) (assistantwire.SpaceMD, string, int, error) {
	e, ok := s.spaceTrack(ctx, slug)
	if !ok {
		return assistantwire.SpaceMD{}, "", http.StatusNotFound, fmt.Errorf("track %q has no SPACE.md", slug)
	}
	machine := s.settings(ctx).MachineID
	if machine == "" || s.isWorkMachine(machine) {
		return assistantwire.SpaceMD{}, "", http.StatusServiceUnavailable, errSpaceMDNode
	}
	if !s.trackSpaceMDNode(machine) {
		return assistantwire.SpaceMD{}, "", http.StatusServiceUnavailable, i18n.Err("assistant-health.node_no_track_spaces", i18n.Args{"machine": s.machineName(machine)})
	}
	c.Dir, c.Slug = trackRel(e), e.Slug
	var out assistantwire.SpaceMD
	err := s.call(ctx, machine, assistantwire.DeviceAssistantSpaceMD, c, &out)
	if err == nil && out.Refused == assistantwire.SpaceMDMissing && c.Op == assistantwire.SpaceMDGet {
		// A track made while its computer was away has no directory yet: make it, then read again.
		if _, perr := s.prepareSpace(ctx, machine, e.Slug); perr == nil {
			out = assistantwire.SpaceMD{}
			err = s.call(ctx, machine, assistantwire.DeviceAssistantSpaceMD, c, &out)
		}
	}
	if err != nil {
		var re *rpc.Error
		if errors.Is(err, apierr.ErrOffline) || errors.Is(err, rpc.ErrClosed) || errors.Is(err, ErrNoDevice) ||
			errors.As(err, &re) && (re.Code == proto.CodeNodeOutdated || re.Code == assistantwire.CodeMachineOffline) {
			return assistantwire.SpaceMD{}, machine, http.StatusServiceUnavailable, errSpaceMDNode
		}
		return assistantwire.SpaceMD{}, machine, http.StatusBadGateway, err
	}
	return out, machine, http.StatusOK, nil
}

// spaceMDRefusal is the HTTP status and the words of a node's refusal (assistantwire.SpaceMD.Refused).
func spaceMDRefusal(why string) (int, error) {
	switch why {
	case assistantwire.SpaceMDStale:
		return http.StatusConflict, errors.New("SPACE.md changed since it was read: read it again")
	case assistantwire.SpaceMDManaged:
		return http.StatusBadRequest, errors.New("the blocks Persona keeps in SPACE.md (between persona:managed markers) and their markers stay as they are")
	case assistantwire.SpaceMDTooLarge:
		return http.StatusRequestEntityTooLarge, errors.New("SPACE.md is larger than 64 KB: edit it on the computer")
	case assistantwire.SpaceMDNoState:
		return http.StatusConflict, errors.New("SPACE.md has no state block (persona:managed:state)")
	case assistantwire.SpaceMDMissing:
		return http.StatusNotFound, errors.New("the track's directory or its SPACE.md is not there")
	}
	return http.StatusBadGateway, fmt.Errorf("SPACE.md: the computer refused (%s)", why)
}

// answerSpaceMD answers out (Refused never goes out).
func answerSpaceMD(w http.ResponseWriter, out assistantwire.SpaceMD) {
	out.Refused = ""
	apierr.WriteJSON(w, out)
}

func (s *Service) trackSpaceMDGet(w http.ResponseWriter, r *http.Request) {
	out, _, status, err := s.spaceMD(r.Context(), r.PathValue("slug"), assistantwire.SpaceMDCall{Op: assistantwire.SpaceMDGet})
	if err != nil {
		apierr.Write(w, status, err)
		return
	}
	if out.Refused != "" {
		status, err := spaceMDRefusal(out.Refused)
		apierr.Write(w, status, err)
		return
	}
	answerSpaceMD(w, out)
}

func (s *Service) trackSpaceMDPut(w http.ResponseWriter, r *http.Request) {
	if !spacesWriter(w, r) {
		return
	}
	var in assistantwire.SpaceMDPut
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, spaceMDBodyMax))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		apierr.Write(w, http.StatusBadRequest, err)
		return
	}
	switch {
	case strings.TrimSpace(in.BaseDigest) == "":
		apierr.Write(w, http.StatusBadRequest, errors.New("base_digest: the digest of the SPACE.md this edit was made on"))
		return
	case !utf8.ValidString(in.Content):
		apierr.Write(w, http.StatusBadRequest, errors.New("content: not UTF-8 text"))
		return
	}
	ctx := r.Context()
	slug := r.PathValue("slug")
	out, machine, status, err := s.spaceMD(ctx, slug, assistantwire.SpaceMDCall{Op: assistantwire.SpaceMDPutHuman, Content: in.Content, BaseDigest: in.BaseDigest})
	if err != nil {
		apierr.Write(w, status, err)
		return
	}
	if out.Refused != "" {
		status, err := spaceMDRefusal(out.Refused)
		apierr.Write(w, status, err)
		return
	}
	// The track's instructions end with its SPACE.md: written again now rather than at the next refresh.
	if _, err := s.prepareSpace(ctx, machine, slug); err != nil {
		s.logf("tracks: prepare %q on %s after its SPACE.md changed: %v", slug, machine, err)
	}
	answerSpaceMD(w, out)
}

// spaceTools are the tools conversation session on machine gets for being in a space track (spaceTrack): the one
// that replaces its SPACE.md's state block. None for any other conversation, the main chat's and the health space's
// included, so their lists stay as they were.
func (s *Service) spaceTools(ctx context.Context, machine, session string) []proto.Tool {
	if session == "" || !s.sameMachine(machine, s.settings(ctx).MachineID) {
		return nil
	}
	space, ok := s.selfSpace(ctx, session)
	if !ok || space == "" || space == SpaceHealth {
		return nil
	}
	if _, ok := s.spaceTrack(ctx, space); !ok {
		return nil
	}
	return toolsNamed([]string{spaceStateTool})
}

// toolSpaceStateUpdate replaces the state block of the SPACE.md of the space track conversation self is in. The list
// offers it only there (spaceTools); a call from anywhere else is refused here as well.
func (s *Service) toolSpaceStateUpdate(ctx context.Context, machine, self string, args json.RawMessage) (string, error) {
	_, guest, err := s.guestOf(ctx, machine, self)
	if err != nil || guest || s.isWorkMachine(machine) || len(s.spaceTools(ctx, machine, self)) == 0 {
		return "", errors.New(spaceStateTool + " is only for the conversations inside a space")
	}
	if why := s.spaceStateRefusal(ctx, machine, self); why != "" {
		return "", errors.New(why)
	}
	var p struct {
		Text string `json:"text"`
	}
	if err := strictParams(spaceStateTool, args, &p); err != nil {
		return "", err
	}
	text := strings.TrimRight(p.Text, "\r\n")
	switch {
	case !utf8.ValidString(text):
		return "", errors.New("text: not UTF-8 text")
	case len(text) > assistantwire.SpaceMDStateMax:
		return "", fmt.Errorf("text is %d bytes; the state block holds at most %d: keep only the current state", len(text), assistantwire.SpaceMDStateMax)
	}
	space, _ := s.selfSpace(ctx, self)
	out, _, _, err := s.spaceMD(ctx, space, assistantwire.SpaceMDCall{Op: assistantwire.SpaceMDPutState, State: text})
	if err != nil {
		return "", err
	}
	switch out.Refused {
	case "":
	case assistantwire.SpaceMDManaged:
		return "", errors.New("text may not hold a persona:managed marker")
	case assistantwire.SpaceMDNoState:
		return "", errors.New("SPACE.md has no state block: the user took it out, so it is not written; tell the user if it matters")
	default:
		_, err := spaceMDRefusal(out.Refused)
		return "", err
	}
	// The instructions are written again from it at the next refresh (refreshInstructions), not in the middle of this
	// turn.
	return "The state block of SPACE.md now holds what you gave; the rest of SPACE.md is unchanged, and the previous file is kept in .space/history/.", nil
}

// spaceStateRefusal says why conversation self on machine, inside a space (spaceTools), may not write its SPACE.md's
// state block, "" when it may. The state block ends the space's instructions, so what writes it is what other people's
// words must not reach: a conversation that talks with others (the inbox's AI twin, a Feishu /agent) or that an
// extension opened for its own ends (outward_reader.go) does not write it. A conversation the extension contributing
// the space's kind opened in that space is the space's own, as the user's in the app is: that extension is the space
// product itself (space-conversations opens each of a space's conversations from the user's message on its page).
// It is refused while where the conversation comes from cannot be told, as outwardReader is.
func (s *Service) spaceStateRefusal(ctx context.Context, machine, self string) string {
	if !isOutwardReader(ctx) {
		return ""
	}
	refused := spaceStateTool + " is only for the space's own conversations: this one talks with other people or was opened by an extension for its own use"
	b := &s.origins
	b.mu.Lock()
	if !s.originsLocked(ctx) {
		b.mu.Unlock()
		return refused
	}
	o := b.m[self]
	b.mu.Unlock()
	if o == nil || o.Origin != OriginExtension || o.Ext == "" || !s.sameMachine(machine, o.MachineID) {
		return refused
	}
	space, _ := s.selfSpace(ctx, self)
	e, ok := s.spaceTrack(ctx, space)
	if !ok {
		return refused
	}
	if k, ok := kindOf(s.trackKinds(ctx), e.Kind); !ok || !k.Extension || k.Owner == "" || k.Owner != o.Ext {
		return refused
	}
	return ""
}
