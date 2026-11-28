package node

import (
	"cmp"
	"path/filepath"
	"slices"
	"strings"

	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/proto"
)

// One folder, one space (#13533). Each way an Agent runs names its space its own way: a terminal host by its workspace
// (herdr's w1), the desktop apps by their project (projectSpace), a runtime with no workspaces of its own by its
// directory (agy, DSH, opencode outside a terminal: space_id is the directory). The hub and every client group a
// computer's sessions by space_id, so a folder whose Agents ran in herdr, in the Claude desktop app and on the agy
// runtime was three projects with one name. Once the list is built, every session whose space only stands for a folder
// (spaceFolder) is put in that folder's space on this computer: the terminal workspace rooted in it (folderWorkspaces),
// else the folder's own, projectSpace and the folder's physical path. A session keeps its space_kind, which says what
// it runs in; only its place changes. Folders are compared by their physical paths (sessionWorkspaceCwd: symbolic links
// resolved, as a home on another volume is reached), and a folder this computer does not have (a daemon's on another
// machine, a folder removed since) keeps the space its session named.

// codexAppKind is the space_kind of the Codex App's chats (decorateCodexSession): its projects have names and an order of
// their own, which the folder's own space keeps.
const codexAppKind = "codex_app"

// spaceFolder is the folder s's space stands for: a desktop app's project (projectSpace and its root), or the directory a
// runtime that keeps no workspaces names its space by. "" for a space a host keeps (a terminal host's workspace, an
// engine's project: Paseo, Cindy, T3 Code), for no space, and for the assistant's own conversations, which are in no
// project (fillGitPlace).
func spaceFolder(s *proto.Session) string {
	if s.Assistant || s.SpaceID == "" {
		return ""
	}
	dir, project := strings.CutPrefix(s.SpaceID, projectSpace)
	if !project {
		if _, engine := engineRuntimes[s.SourceInstanceID]; s.SourceKind != proto.SourceRuntime || engine {
			return ""
		}
	}
	if !filepath.IsAbs(dir) {
		return ""
	}
	return dir
}

// folderWorkspace is a workspace of the terminal host of kind.
type folderWorkspace struct {
	kind  string
	space host.Space
}

// terminalWorkspaces are the workspaces the terminal hosts' last snapshots say a directory of.
func (n *Node) terminalWorkspaces() []folderWorkspace {
	n.mu.Lock()
	defer n.mu.Unlock()
	var out []folderWorkspace
	for kind, l := range n.layouts {
		for _, w := range l.spaces {
			if w.Cwd != "" {
				out = append(out, folderWorkspace{kind, w})
			}
		}
	}
	return out
}

// joinFolderSpaces puts each session of list whose space stands for a folder in that folder's space (placeInFolders).
// A list with no such session is left as it is, with nothing looked up.
func (n *Node) joinFolderSpaces(list []proto.Session) {
	for i := range list { // by index: a range value copies every (large) session it passes
		if spaceFolder(&list[i]) != "" {
			placeInFolders(list[i:], n.terminalWorkspaces(), n.defaultKind(), n.sessionWorkspaceCwd)
			return
		}
	}
}

// placeInFolders puts each session of list whose space stands for a folder (spaceFolder) that physical resolves (its
// physical path, "" for one this computer does not have) in the folder's space: the workspace of workspaces rooted in it
// (folderWorkspaces), with its id, name, number and parent, as the host's own sessions there have them; else the
// folder's own, projectSpace and the physical path, named as the Codex App names its project there (and numbered by
// its place in the App's sidebar), else by the folder. def is the default engine's kind.
func placeInFolders(list []proto.Session, workspaces []folderWorkspace, def string, physical func(string) string) {
	type own struct {
		name  string
		index int32
	}
	type row struct {
		i   int
		dir string
	}
	joined := folderWorkspaces(workspaces, def, physical)
	var rows []row
	var owns map[string]own
	for i := range list {
		s := &list[i]
		folder := spaceFolder(s)
		if folder == "" {
			continue
		}
		dir := physical(folder)
		if dir == "" {
			continue
		}
		rows = append(rows, row{i, dir})
		k := folderKey(dir)
		if _, ok := joined[k]; ok || s.SpaceKind != codexAppKind || !strings.HasPrefix(s.SpaceID, projectSpace) || s.SpaceName == "" {
			continue
		}
		// One name for a folder whichever chat gives it (two Codex homes can name one folder): the one the App
		// numbers first, then the name that sorts first.
		o := own{s.SpaceName, s.SpaceIndex}
		if had, ok := owns[k]; ok && !numberedBefore(o.index, o.name, had.index, had.name) {
			continue
		}
		if owns == nil {
			owns = map[string]own{}
		}
		owns[k] = o
	}
	for _, r := range rows {
		s := &list[r.i]
		k := folderKey(r.dir)
		if w, ok := joined[k]; ok {
			s.SpaceID, s.SpaceName, s.SpaceIndex, s.ParentSpaceID = w.ID, w.Label, int32(w.Number), w.ParentID
			continue
		}
		o, ok := owns[k]
		if !ok {
			o = own{name: filepath.Base(r.dir)}
		}
		s.SpaceID, s.SpaceName, s.SpaceIndex, s.ParentSpaceID = projectSpace+r.dir, o.name, o.index, ""
	}
}

// numberedBefore: place a (its index and name) sorts before place b: a numbered place before one with no number, a lower
// number first, then the name.
func numberedBefore(ai int32, an string, bi int32, bn string) bool {
	if (ai > 0) != (bi > 0) {
		return ai > 0
	}
	if ai != bi {
		return ai < bi
	}
	return an < bn
}

// folderWorkspaces is the workspace each folder's other sessions join, by the folder's key (folderKey of its physical
// path), of the workspaces rooted in it. A host's own workspace goes before a directory group (dirSpace: herdr-core's
// terminals outside the app's spaces, botmux's topics). Of several, the one named after its folder, as New Agent reuses
// it for the folder (workspaceFor): the default engine's first, then by host and the host's numbering. A folder with
// several workspaces and none named after it joins none, since which one is the folder's is not known.
func folderWorkspaces(workspaces []folderWorkspace, def string, physical func(string) string) map[string]host.Space {
	if len(workspaces) == 0 {
		return nil
	}
	byFolder := map[string][]folderWorkspace{}
	for _, w := range workspaces {
		if !filepath.IsAbs(w.space.Cwd) {
			continue
		}
		if dir := physical(w.space.Cwd); dir != "" {
			k := folderKey(dir)
			byFolder[k] = append(byFolder[k], w)
		}
	}
	out := make(map[string]host.Space, len(byFolder))
	for k, ws := range byFolder {
		if w, ok := folderWorkspaceOf(ws, def); ok {
			out[k] = w.space
		}
	}
	return out
}

// folderWorkspaceOf picks the folder's workspace of the workspaces rooted in it (folderWorkspaces).
func folderWorkspaceOf(ws []folderWorkspace, def string) (folderWorkspace, bool) {
	group := func(w folderWorkspace) bool { return strings.HasPrefix(w.space.ID, dirSpace) }
	if slices.ContainsFunc(ws, func(w folderWorkspace) bool { return !group(w) }) {
		ws = slices.DeleteFunc(ws, group)
	}
	if len(ws) == 1 {
		return ws[0], true
	}
	slices.SortFunc(ws, func(a, b folderWorkspace) int {
		return cmp.Or(boolRank(b.kind == def)-boolRank(a.kind == def), strings.Compare(a.kind, b.kind),
			cmp.Compare(a.space.Number, b.space.Number), strings.Compare(a.space.ID, b.space.ID))
	})
	for _, w := range ws {
		if w.space.Label == filepath.Base(w.space.Cwd) {
			return w, true
		}
	}
	return folderWorkspace{}, false
}
