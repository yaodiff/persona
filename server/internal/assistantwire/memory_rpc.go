package assistantwire

import (
	"time"

	"github.com/nwverse/persona/server/internal/buswire"
)

// MethodAssistantMemory lists the assistant's memory entries for an extension granted assistant.memory.read
// (docs/extensions/extensions.md "Assistant memory"): the hub checks the grant and forwards it as it is; the manager,
// which reaches the assistant's computer, answers it. Read-only, and only the assistant's own space: the health
// space's memory is never listed.
const MethodAssistantMemory = "assistant.memory"

var AssistantMemoryMethod = buswire.Method[AssistantMemoryParams, AssistantMemoryResult]{Name: MethodAssistantMemory}

// Bounds of assistant.memory: each entry's content is cut at AssistantMemoryEntryMax bytes, and contents stop being
// included once a page holds AssistantMemoryTotalMax bytes of them (the entries after it come without content, with
// truncated set).
const (
	AssistantMemoryEntryMax = 16 << 10
	AssistantMemoryTotalMax = 512 << 10
)

// AssistantMemoryParams is assistant.memory's params.
type AssistantMemoryParams struct {
	// IncludeContent: return each entry's text (bounded); without it only the metadata.
	IncludeContent bool `json:"include_content,omitempty"`
}

// AssistantMemoryEntry is one memory entry: one Markdown file, its frontmatter read.
type AssistantMemoryEntry struct {
	Name        string `json:"name"`                  // the file's name, e.g. "travel-preferences.md"
	Description string `json:"description,omitempty"` // the frontmatter's description; empty without one
	Type        string `json:"type,omitempty"`        // the frontmatter's type (metadata.type too); empty without one
	Size        int    `json:"size"`                  // the file's size in bytes
	// Created is when the file was made, when the computer's file system says (absent otherwise).
	Created  *time.Time `json:"created,omitempty"`
	Modified time.Time  `json:"modified"`
	// Content is the file's text, with include_content; cut at AssistantMemoryEntryMax bytes or left out past
	// AssistantMemoryTotalMax (Truncated says so).
	Content   string `json:"content,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`
	// Track is the track whose memory it is ("" the main track's); an extension is shown another track's only when it
	// may read what comes from it (docs/assistant/health.md "Using information by scene", scene 6).
	Track string `json:"track,omitempty"`
}

// AssistantMemoryResult is assistant.memory's result, by name. An assistant without memory (none written yet, or
// the feature off) has no entries.
type AssistantMemoryResult struct {
	Entries []AssistantMemoryEntry `json:"entries"`
}
