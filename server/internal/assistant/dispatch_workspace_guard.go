package assistant

import (
	"context"
	"path"

	"github.com/nwverse/persona/server/internal/identity"
	"github.com/nwverse/persona/server/internal/proto"
)

func (s *Service) autoTaskUsesAssistantWorkspace(ctx context.Context, t *PlanTask) bool {
	existing := ""
	if t.Workspace != nil && t.Workspace.Mode == workspaceExisting {
		existing = t.Workspace.Existing
	}
	for _, dir := range []string{t.Cwd, existing} {
		if assistantWorkspace(Device{}, dir) {
			return true
		}
	}
	for _, d := range s.devices(ctx) {
		for _, dir := range []string{t.Cwd, existing} {
			if assistantWorkspace(d, proto.ExpandHome(dir, d.Home)) {
				return true
			}
		}
	}
	return false
}

// The node resolves arbitrary aliases. The hub also knows custom state roots
// from node metadata and must reject those before publishing a plan.
func assistantWorkspace(m Device, cwd string) bool {
	if identity.IsAssistantDir(cwd) {
		return true
	}
	clean := path.Clean(identity.NodePath(cwd))
	name := path.Base(clean)
	data := path.Clean(identity.NodePath(m.DataDir))
	if identity.ValidTrackSlug(name) && identity.IsManagerDirName(path.Base(path.Dir(clean)), "tracks") {
		return m.DataDir != "" && path.Dir(path.Dir(clean)) == data // a track's directory (tracks/<slug>, #13472)
	}
	if !identity.IsManagerDirName(name, "assistant") && !identity.IsManagerDirName(name, identity.SpaceHealth) {
		return false
	}
	return m.DataDir != "" && path.Dir(clean) == data
}
