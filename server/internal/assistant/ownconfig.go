package assistant

import (
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/aiwire"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/managerconf"
	"github.com/nwverse/persona/server/internal/proto"
)

// ownConfig is what the manager's manager.json says that the hub's hello used to (docs/operations/config.md "manager.json"):
// whose assistant it is, where it runs by default, the Agents business's limits, the knowledge base, the models and how
// long a card waits. A section manager.json has replaces the hello's; one it has not keeps it (an older hub still
// sends them).
type ownConfig struct {
	ProjectReview projectReviewConfig `json:"project_review"`
	Assistant     *struct {
		Owner                         *string `json:"owner"`
		Notes                         *string `json:"notes"`
		Machine                       *string `json:"machine"`
		Launcher                      *string `json:"launcher"`
		MemoryDreaming                bool    `json:"memory_dreaming"`
		CompleteAgentlessTodos        bool    `json:"complete_agentless_todos"`
		AutoApproveReversibleSettings bool    `json:"auto_approve_reversible_settings"`
		SupervisionBatchMinutes       *int    `json:"supervision_batch_minutes"`
		TaskExecutionWaitSeconds      *int    `json:"task_execution_wait_seconds"`
		TaskExecutionLegacyFallback   *bool   `json:"task_execution_legacy_fallback"`
		NodeUpdateHandoffGraceSeconds *int    `json:"node_update_handoff_grace_seconds"`
	} `json:"assistant"`
	Agents *assistantwire.AgentsConfig `json:"agents"`
	KB     *struct {
		ReadOnly       bool                           `json:"read_only"`
		Enabled        *bool                          `json:"enabled"`
		TextDays       int                            `json:"text_days"`
		AttachmentDays int                            `json:"attachment_days"`
		AI             *aiwire.Config                 `json:"ai"`
		ExtraDirs      []string                       `json:"extra_dirs"`
		RemoteFolders  []assistantwire.KBRemoteFolder `json:"remote_folders"`
		// AttachmentDaysBySource: attachment_days per source, e.g. years for a chat source whose history is imported.
		AttachmentDaysBySource map[string]int `json:"attachment_days_by_source"`
		// History: the history import (docs/assistant/knowledge.md "History import"); missing or not enabled: off.
		History        *assistantwire.KBHistory        `json:"history"`
		ImportProvider *assistantwire.KBImportProvider `json:"import_provider"`
		// LarkMinutes: importing Lark minutes and document titles through lark-cli; missing or not enabled: off.
		LarkMinutes *assistantwire.KBLarkMinutes `json:"lark_minutes"`
		// SpeakerTurnsImport: debug-only switch enabling PUT /api/v1/recordings/{id}/speakers/turns (#17674); off by
		// default, read once at manager start, and never changeable from an app or PUT /api/v1/assistant.
		SpeakerTurnsImport bool `json:"speaker_turns_import"`
	} `json:"kb"`
	AI *aiwire.Config `json:"ai"`
	// Inbox: of it the hello carried the models and the AI twin's machine, each replaced only when it is there (the
	// rest of inbox is the extensions', extmgr.Config).
	Inbox *struct {
		TriageAI *aiwire.Config `json:"triage_ai"`
		JudgeAI  *aiwire.Config `json:"judge_ai"`
		Agent    struct {
			Machine *string `json:"machine"`
		} `json:"agent"`
	} `json:"inbox"`
	HookTimeoutMinutes *int `json:"hook_timeout_minutes"`
}

// readOwnConfig reads file's sections; a missing file is none (the hello's stand). One that does not read is an
// error: the manager does not start with it (moveConfig), and one running keeps what it last read (setHello).
func readOwnConfig(file string) (ownConfig, error) {
	var c ownConfig
	if file == "" {
		return c, nil
	}
	b, err := managerconf.Sections(file, "project_review")
	if err != nil {
		if os.IsNotExist(err) {
			return c, nil
		}
		return c, err
	}
	if err := json.Unmarshal(b, &c); err != nil {
		return c, err
	}
	if a := c.Assistant; a != nil {
		if a.SupervisionBatchMinutes != nil {
			if minutes := *a.SupervisionBatchMinutes; minutes < 0 || minutes > maxSupervisionBatchMinutes {
				return c, fmt.Errorf("assistant.supervision_batch_minutes must be between 0 and %d", maxSupervisionBatchMinutes)
			}
		}
		if a.TaskExecutionWaitSeconds != nil && (*a.TaskExecutionWaitSeconds < 0 || *a.TaskExecutionWaitSeconds > 3600) {
			return c, fmt.Errorf("assistant.task_execution_wait_seconds must be between 0 and 3600")
		}
		if a.NodeUpdateHandoffGraceSeconds != nil && (*a.NodeUpdateHandoffGraceSeconds < 0 || *a.NodeUpdateHandoffGraceSeconds > proto.MaxNodeUpdateHandoffGraceSeconds) {
			return c, fmt.Errorf("assistant.node_update_handoff_grace_seconds must be between 0 and %d", proto.MaxNodeUpdateHandoffGraceSeconds)
		}
	}
	return c, c.ProjectReview.validate()
}

// validatePlacement rejects a configured assistant machine that the hub classifies as work.
func (c ownConfig) validatePlacement(h Hello) error {
	if a := c.Assistant; a != nil && a.Machine != nil && slices.Contains(h.Assistant.WorkMachines, *a.Machine) {
		return i18n.Err("machine-trust.assistant_machine", i18n.Args{"machine": *a.Machine})
	}
	return nil
}

// over is h with c's sections in place of the hello's.
func (c ownConfig) over(h Hello) Hello {
	// This approval bypass is manager-file-only: unlike legacy hello sections, a missing assistant section must not
	// preserve a value supplied by the hub.
	h.Assistant.AutoApproveReversibleSettings = c.Assistant != nil && c.Assistant.AutoApproveReversibleSettings
	if a := c.Assistant; a != nil {
		h.Assistant.MemoryDreaming = a.MemoryDreaming
		h.Assistant.CompleteAgentlessTodos = a.CompleteAgentlessTodos
		if a.SupervisionBatchMinutes != nil {
			h.Assistant.SupervisionBatchMinutes = a.SupervisionBatchMinutes
		}
		if a.TaskExecutionWaitSeconds != nil {
			h.Assistant.TaskExecutionWaitSeconds = a.TaskExecutionWaitSeconds
		}
		if a.TaskExecutionLegacyFallback != nil {
			h.Assistant.TaskExecutionLegacyFallback = a.TaskExecutionLegacyFallback
		}
		if a.NodeUpdateHandoffGraceSeconds != nil {
			h.Assistant.NodeUpdateHandoffGraceSeconds = a.NodeUpdateHandoffGraceSeconds
		}
		if a.Owner != nil {
			h.Owner = strings.TrimSpace(*a.Owner)
		}
		if a.Notes != nil {
			h.OwnerNotes = strings.TrimSpace(*a.Notes)
		}
		if a.Machine != nil && slices.Contains(h.Assistant.Machines, *a.Machine) && !slices.Contains(h.Assistant.WorkMachines, *a.Machine) {
			h.Assistant.MachineID = *a.Machine
		}
		if a.Launcher != nil {
			h.Assistant.Launcher = *a.Launcher
		}
	}
	if c.Agents != nil {
		h.Agents = *c.Agents
	}
	if k := c.KB; k != nil {
		h.KB = &assistantwire.KBSettings{Off: k.Enabled != nil && !*k.Enabled, ReadOnly: k.ReadOnly, TextDays: k.TextDays,
			AttachmentDays: k.AttachmentDays, ExtraDirs: k.ExtraDirs, RemoteFolders: k.RemoteFolders, AttachmentDaysBySource: positiveDays(k.AttachmentDaysBySource), History: k.History,
			LarkMinutes: k.LarkMinutes, ImportProvider: k.ImportProvider}
		h.Models.KBAI = k.AI
	}
	if c.AI != nil {
		h.Models.AI = *c.AI
	}
	if in := c.Inbox; in != nil {
		if in.TriageAI != nil {
			h.Models.TriageAI = in.TriageAI
		}
		if in.JudgeAI != nil {
			h.Models.JudgeAI = in.JudgeAI
		}
		if m := in.Agent.Machine; m != nil {
			h.Models.InboxAgentMachine = ""
			if slices.Contains(h.Assistant.Machines, *m) {
				h.Models.InboxAgentMachine = *m
			}
		}
	}
	if m := c.HookTimeoutMinutes; m != nil {
		minutes := *m
		if minutes <= 0 { // as hub.json's always was: 0 or below is the default
			minutes = 30
		}
		h.Notify.HookTimeoutMS = (time.Duration(minutes) * time.Minute).Milliseconds()
	}
	if slices.Contains(h.Assistant.WorkMachines, h.Assistant.MachineID) {
		h.Assistant.MachineID = ""
	}
	if h.KB == nil { // no kb section anywhere: the knowledge base with its defaults (the hub always said so)
		h.KB = &assistantwire.KBSettings{}
	}
	return h
}

// positiveDays is m without its non-positive values (a source with 0 days keeps attachment_days); nil when none is
// left, so a section without the setting compares equal to one with an empty map.
func positiveDays(m map[string]int) map[string]int {
	var out map[string]int
	for k, v := range m {
		if v > 0 {
			if out == nil {
				out = map[string]int{}
			}
			out[k] = v
		}
	}
	return out
}
