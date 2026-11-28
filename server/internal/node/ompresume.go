package node

// Resuming a stopped omp (docs/integrations/omp.md): `omp --resume <session file>` in a new herdr tab, then the phone's
// message once the extension lists the session again. The file comes from omp's sessions on disk (ompdisk.go) or from
// what herdr's omp integration reported while it ran; a file is never taken from the client, and one that is gone, has
// an apostrophe (herdr's resume command cannot be said) or lies outside the agent's sessions directory is refused
// rather than guessed at.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

const ompResumeUnknownNote = "这个 omp 会话找不到会话文件，请在电脑上恢复，这次没有发送。"

// orcaResumeRefusal refuses a resume Orca does not carry out from the phone: an omp session's. Nothing was started or
// sent; the hub words it in the request's language (apierr, rpc-errors.orca_resume).
func orcaResumeRefusal(ctx context.Context) error {
	return refusal("orca_resume", i18n.T(i18n.From(ctx), "rpc-errors.orca_resume"), nil)
}

// ompKnownFile is the session file of id the node knows about: from the disk scan, else what herdr reported.
func (n *Node) ompKnownFile(id string) string {
	if p := n.ompSessionPath(id); p != "" && ompResumePath(n.home, p) {
		return p
	}
	if s, ok := n.ompDiskSession(id); ok && ompResumePath(n.home, s.Path) { // a live one's file, for herdr's restore
		return s.Path
	}
	if s, ok := n.ompSession(id); ok && ompResumePath(n.home, s.Path) {
		return s.Path
	}
	return ""
}

// ompSessionPath is the session file herdr's omp integration reported for id.
func (n *Node) ompSessionPath(id string) string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.ompFiles[id]
}

// ompResumeSession is what resuming id needs: the session file and the directory to open the tab in.
func (n *Node) ompResumeSession(id string) (ompDiskSession, bool) {
	if s, ok := n.ompSession(id); ok {
		if !ompResumePath(n.home, s.Path) {
			return ompDiskSession{}, false
		}
		if st, err := os.Stat(s.Path); err != nil || st.IsDir() {
			return ompDiskSession{}, false
		}
		return s, true
	}
	p := n.ompSessionPath(id)
	if p == "" || !ompResumePath(n.home, p) {
		return ompDiskSession{}, false
	}
	s := readOmpSession(p)
	if s.ID != id || s.Cwd == "" {
		return ompDiskSession{}, false
	}
	if st, err := os.Stat(p); err != nil || st.IsDir() {
		return ompDiskSession{}, false
	}
	return s, true
}

// ompResumePath says whether path may be run as an omp resume: a .jsonl file under <agent dir>/sessions, without an
// apostrophe.
func ompResumePath(home, path string) bool {
	if path == "" || !strings.HasSuffix(path, ".jsonl") || strings.Contains(path, "'") {
		return false
	}
	root := filepath.Join(ompAgentDir(home), "sessions")
	rel, err := filepath.Rel(root, filepath.Clean(path))
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// ompResumeSendKey carries what a session.start resuming a session brought besides its prompt (node.go): the images and
// the client_msg_id, so the message a resumed omp gets is the one the client sent, deduplicated like any send.
type ompResumeSendKey struct{}

type ompResumeSend struct {
	images      []proto.Image
	clientMsgID string
}

func withOmpResumeSend(ctx context.Context, images []proto.Image, clientMsgID string) context.Context {
	if len(images) == 0 && clientMsgID == "" {
		return ctx
	}
	return context.WithValue(ctx, ompResumeSendKey{}, ompResumeSend{images: images, clientMsgID: clientMsgID})
}

// ompResumeParams is the session.send a session.start resuming id makes of its prompt: nil when it brought nothing to
// send (the session is started to be watched).
func ompResumeParams(ctx context.Context, id, prompt string) json.RawMessage {
	extra, _ := ctx.Value(ompResumeSendKey{}).(ompResumeSend)
	if prompt == "" && len(extra.images) == 0 {
		return nil
	}
	m := map[string]any{"session_id": id, "text": prompt}
	if len(extra.images) > 0 {
		m["images"] = extra.images
	}
	if extra.clientMsgID != "" {
		m["client_msg_id"] = extra.clientMsgID
	}
	b, _ := json.Marshal(m)
	return b
}

// resumeOmp resumes a stopped omp in herdr and sends the phone's message there. Without a usable session file nothing
// is started and nothing is sent, nor with Orca the default engine, where resumeInTerminal would start it: an omp is
// not resumed in Orca (#13597).
func (n *Node) resumeOmp(ctx context.Context, id string, params json.RawMessage) (any, error) {
	s, ok := n.ompResumeSession(id)
	if !ok {
		return nil, refusal("omp_resume_unknown", ompResumeUnknownNote, nil)
	}
	if n.defaultKind() == proto.EngineOrca {
		return nil, orcaResumeRefusal(ctx)
	}
	return n.resumeInTerminal(ctx, s.Cwd, []string{ompAgent, "--resume", s.Path}, id, params,
		func(owner *runtimeConn) bool { return owner != nil })
}
