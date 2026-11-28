package assistant

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/nwverse/persona/server/internal/kb"
)

// projectTreeText is a project's place in the project tree for kb_get (#12927): its parent and the way down to it, the
// projects under it and its milestones, each child and milestone on one line.
func projectTreeText(t kb.ProjectTree) string {
	if t.Parent == nil && len(t.Children) == 0 && len(t.Milestones) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\nProject tree:\n")
	if t.Parent != nil {
		var way []string
		for _, a := range t.Ancestors {
			way = append(way, a.Name+" ("+a.ID+")")
		}
		fmt.Fprintf(&b, "- parent: %s · level %d\n", strings.Join(way, " › "), t.Depth)
	}
	for _, c := range t.Children {
		line := "- child: " + c.ID + " · " + c.Name + " [" + c.Status + "]"
		if c.Children > 0 {
			line += fmt.Sprintf(" · %d under it", c.Children)
		}
		if c.OneLine != "" {
			line += " — " + c.OneLine
		}
		b.WriteString(line + "\n")
	}
	for _, m := range t.Milestones {
		line := "- milestone " + m.ID + ": " + m.Title + " [" + m.State + "]"
		if m.Target != "" {
			line += " · target " + m.Target
		}
		b.WriteString(line + "\n")
	}
	return b.String()
}

// projectTreeSaved follows kb_update's "Saved" for a parent or milestone change: where the project now sits, a hint
// past the suggested depth, and the call that undoes the change.
func projectTreeSaved(e *kb.Engine, id string, undo *kb.UserUpdate) string {
	var b strings.Builder
	if t, ok := e.ProjectTreeOf(id); ok {
		if t.Parent != nil {
			fmt.Fprintf(&b, "\nNow under %s (%s), level %d.", t.Parent.Name, t.Parent.ID, t.Depth)
		} else {
			b.WriteString("\nNow at the top level.")
		}
		if t.Depth > kb.SuggestedTreeDepth {
			fmt.Fprintf(&b, " More than %d levels deep: allowed, but a shallower tree is easier to read.", kb.SuggestedTreeDepth)
		}
		if len(t.Milestones) > 0 {
			var ms []string
			for _, m := range t.Milestones {
				ms = append(ms, m.ID+" "+m.Title+" ["+m.State+"]")
			}
			b.WriteString("\nMilestones: " + strings.Join(ms, "; ") + ".")
		}
	}
	if undo != nil {
		args := map[string]any{"kind": kb.KindProject, "id": id}
		if undo.Parent != nil {
			args["parent"] = *undo.Parent
		}
		if undo.Milestones != nil {
			args["milestones"] = *undo.Milestones
		}
		raw, _ := json.Marshal(args)
		b.WriteString("\nTo undo, if the user asks: kb_update " + string(raw))
	}
	return b.String()
}
