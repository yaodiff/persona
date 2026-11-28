package prompts

// StandingMemory is the snapshot section of the memory entries a main-space conversation keeps at hand
// (assistant/snapshot_memory, #13586, docs/assistant/health.md "Memory by scene"): the memory extension's standing
// entries the assembler let through for it. Each text is on one line, without the closing tags ("</persona-context>").
type StandingMemory struct {
	Language string // the answer language's own name; "" is Chinese
	Entries  []MemoryEntry
}

// RenderStandingMemory is the section; empty when there are no entries.
func RenderStandingMemory(d StandingMemory) string {
	if len(d.Entries) == 0 {
		return ""
	}
	return render("assistant/snapshot_memory", d)
}

// RenderEntityMemory renders the main-chat entity-triggered local memory section.
func RenderEntityMemory(d StandingMemory) string {
	if len(d.Entries) == 0 {
		return ""
	}
	return render("assistant/snapshot_entity_memory", d)
}

// LeaderProjectMemory is the snapshot section of extension project memory for a leader's bound project.
type LeaderProjectMemory struct {
	Language string
	Project  string
	Entries  []MemoryEntry
}

// RenderLeaderProjectMemory renders the leader project memory section; empty when there are no entries.
func RenderLeaderProjectMemory(d LeaderProjectMemory) string {
	if len(d.Entries) == 0 {
		return ""
	}
	return render("assistant/snapshot_leader_memory", d)
}

// RenderLeaderDecisions renders the scoped active user decisions section for a leader conversation.
func RenderLeaderDecisions(d StandingMemory) string {
	if len(d.Entries) == 0 {
		return ""
	}
	return render("assistant/snapshot_leader_decisions", d)
}
