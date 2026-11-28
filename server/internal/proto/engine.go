package proto

// Terminal engines: the terminal host(s) a machine's node drives, chosen per machine. EngineHerdrBoth runs classic
// herdr and herdr-core side by side (docs/integrations/engines.md "Host sets"): a machine used both locally and through the Herdr
// app's remote sessions.
const (
	EngineHerdr     = "herdr"      // the classic herdr CLI
	EngineHerdrCore = "herdr-core" // the runtime of the Herdr desktop app
	EngineHerdrBoth = "herdr-both" // classic herdr and herdr-core together
	EngineOrca      = "orca"       // the Orca desktop app's runtime
	// EngineTern is Tern, Stencil's multiplexing terminal, through its `tern` command line (node/hosttern.go,
	// docs/integrations/engines.md "Tern").
	EngineTern = "tern"
	// EngineRex is Rex, a multiplexing terminal, through its `rex` command line and control API (node/hostrex.go,
	// docs/integrations/engines.md "Rex").
	EngineRex = "rex"
	// EnginePaseo is not a terminal: the Paseo daemon's agents, through its runtime (node/runtimecontrol.go,
	// docs/integrations/paseo.md). New sessions and sessions to continue go to Paseo.
	EnginePaseo = "paseo"
	// EngineCindy is Cindy's agents, through its runtime bridge (node/cindybridge.go, docs/integrations/engines.md).
	EngineCindy = "cindy"
	// EngineT3Code is T3 Code's threads, through its runtime bridge to the local T3 server (node/t3codebridge.go,
	// docs/integrations/engines.md "T3 Code").
	EngineT3Code = "t3code"
)

// HostAgy is the host a start names to run on the computer's Antigravity companion (the agy runtime's terminal-less
// sessions, node runtimeOfIn) instead of in an engine host. It is a start host beside EngineHostIDs, never an engine a
// machine enables: a new Agent with the default host runs agy in a herdr pane (#10237), a task the assistant starts
// runs it here when the companion is connected (#13447).
const HostAgy = "agy"

// RuntimeEngine: engine is one that is a runtime, not a terminal (Paseo, Cindy, T3 Code): its own providers start every
// session that opens in it. Whether it runs only those (a launcher it lacks gets one of the same kind, or a refusal) its
// node says per start host (launchers.list's host_runtime, #8140).
func RuntimeEngine(engine string) bool {
	return engine == EnginePaseo || engine == EngineCindy || engine == EngineT3Code
}

// EngineNames are the engines a machine may use, with the names the app shows.
var EngineNames = map[string]string{
	EngineHerdr:     "herdr",
	EngineHerdrCore: "Herdr App",
	EngineHerdrBoth: "herdr + Herdr App",
	EngineOrca:      "Orca",
	EngineTern:      "Tern",
	EngineRex:       "Rex",
	EnginePaseo:     "Paseo",
	EngineCindy:     "Cindy",
	EngineT3Code:    "T3 Code",
}

// Engine is one engine as found on a machine: Available when its socket is reachable (herdr: or its binary is there).
// Note says why one that is there cannot be used (Orca too old or too new, Paseo's bridge not connected) or asks to
// update it (Paseo), in Chinese; the hub words the node-engine catalog's notes in each reader's language
// (i18n.EngineText), the others reach the app as they are.
type Engine struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Available bool   `json:"available"`
	Version   string `json:"version,omitempty"`
	Note      string `json:"note,omitempty"`
	// State is what the node found, told apart where Available alone cannot: installed and not running, incompatible,
	// not there. Only Orca's row says it (#17037); the other rows and older nodes leave it out (EngineStateUnknown).
	State EngineState `json:"state,omitempty"`
}

// EventEnginesChanged is the node's notification that what it found of its engines changed since node.info
// (EnginesChanged): Orca opened or quit (node/orcaengine.go, #17037). The manager takes them into the machine.
const EventEnginesChanged = "engines.changed"

// EnginesChanged is EventEnginesChanged's params: the engines as node.info would say them now.
type EnginesChanged struct {
	Engines []Engine `json:"engines"`
}

// EngineState is what a node found of an engine on its machine (Engine.State). The clients show Orca's "isn't
// running" notice for EngineStopped alone; a state this client does not know reads as EngineStateUnknown.
type EngineState string

const (
	// EngineStateUnknown: the row does not say (an older node, an engine whose row has no state).
	EngineStateUnknown EngineState = ""
	// EngineRunning: it answers with a protocol Persona speaks (Available).
	EngineRunning EngineState = "running"
	// EngineStopped: it is installed on the machine and does not answer now (Orca closed).
	EngineStopped EngineState = "stopped"
	// EngineIncompatible: it answers, with a protocol Persona does not speak; Version says which it is, Note what to do.
	EngineIncompatible EngineState = "incompatible"
	// EngineMissing: it was not found on the machine.
	EngineMissing EngineState = "missing"
)

// Known is s, or EngineStateUnknown for a state this build does not know (one from a newer node).
func (s EngineState) Known() EngineState {
	switch s {
	case EngineRunning, EngineStopped, EngineIncompatible, EngineMissing:
		return s
	}
	return EngineStateUnknown
}
