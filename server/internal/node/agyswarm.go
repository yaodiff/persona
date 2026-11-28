package node

import (
	"encoding/json"
	"io"
	"maps"
	"slices"
	"strings"
)

const (
	agySwarmMembersPath = ".system_generated/swarm/members.json"
	agySwarmMembersMax  = 64 << 10
)

type agySwarmMember struct {
	Role     string `json:"role"`
	Phase    string `json:"phase"`
	WakeMode string `json:"wake_mode"`
	Killed   bool   `json:"killed"`
}

type agySwarmWorker struct {
	Name  string `json:"name"`
	State string `json:"state"`
}

func (m agySwarmMember) state() string {
	switch {
	case m.Killed || m.Phase == "STOPPED":
		return "stopped"
	case m.Phase == "RUNNING" && m.WakeMode == "working":
		return "working"
	case m.Phase == "RUNNING" && m.WakeMode == "mentions":
		return "waiting"
	default:
		return "unknown"
	}
}

// agySwarmWorkers reads the current roster for a verified agy conversation.
// Swarm workers can change state without adding a step to the parent's transcript.
func (n *Node) agySwarmWorkers(conversation string) []agySwarmWorker {
	if !validID(conversation) {
		return nil
	}
	brain, err := agyBrain(n.agyRootFor(conversation), conversation)
	if err != nil {
		return nil
	}
	defer brain.Close()
	info, err := brain.Stat(agySwarmMembersPath)
	if err != nil || !info.Mode().IsRegular() || info.Size() > agySwarmMembersMax {
		return nil
	}
	file, err := brain.Open(agySwarmMembersPath)
	if err != nil {
		return nil
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, agySwarmMembersMax+1))
	if err != nil || len(raw) > agySwarmMembersMax {
		return nil
	}
	var roster struct {
		Members map[string]agySwarmMember `json:"members"`
	}
	if json.Unmarshal(raw, &roster) != nil {
		return nil
	}
	var workers []agySwarmWorker
	for _, name := range slices.Sorted(maps.Keys(roster.Members)) {
		member := roster.Members[name]
		if member.Role == "worker" && strings.TrimSpace(name) != "" {
			workers = append(workers, agySwarmWorker{Name: name, State: member.state()})
		}
	}
	return workers
}
