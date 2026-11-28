package prompts

import (
	"strings"
	"testing"
)

// The MCP tools' descriptions are the same for every hub, read as they are (not templates): none may name a folder
// of the default layout, which is somewhere else once PERSONA_HOME or kb_dir moves it.
func TestMCPToolsDoNotNameTheDefaultFolders(t *testing.T) {
	for _, n := range ToolNames() {
		if d := Tool(n).Description; strings.Contains(d, "~/.persona") {
			t.Errorf("%s: the description names a default folder: %q", n, d)
		}
	}
}

// Retention can leave minutes readable long after a recording's transcript expires (#3780, #3862).
func TestRecordingToolDescriptionsRespectRetention(t *testing.T) {
	for _, name := range []string{"kb_get", "kb_list"} {
		t.Run(name, func(t *testing.T) {
			description := Tool(name).Description
			for _, stale := range []string{"full timestamped transcript", "recordings of the last days", "kept a few days"} {
				if strings.Contains(description, stale) {
					t.Errorf("description promises expired material: %q", stale)
				}
			}
			for _, want := range []string{"recording_text_days", "minutes_days", "forever by default"} {
				if !strings.Contains(description, want) {
					t.Errorf("description does not explain retention %q: %s", want, description)
				}
			}
		})
	}
}
