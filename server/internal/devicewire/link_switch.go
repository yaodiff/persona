package devicewire

import "github.com/nwverse/persona/server/internal/buswire"

// Switching an ssh computer to Persona Link (docs/operations/link.md "Switching from SSH", #10662). The manager that
// carries the computer runs the switch over its own ssh access; the hub owns what only it may change: the kind in the
// machine's entry and the computer's pairing code and credential. Two calls, each one step on the hub:
//
//   - begin: the entry still says what the manager last saw (kind ssh, the same ssh_host) and the computer has no
//     Persona Link credential or unused code yet; the hub sets kind link (every other field as it was) and makes one
//     pairing code for this machine, which it answers with. The computer redeems it once, as any pairing link.
//   - end: succeeded keeps it all; rolled_back revokes this switch's code and the credential it was traded for (all of
//     the machine's link credentials and codes when PairHash is unknown: begin refused one that had any) and puts the
//     kind back to ssh. Repeating either answers the same.
//
// The pairing link goes only to the computer, over ssh on its installer's stdin; never into a log, an answer to the
// apps or the manager's store (it keeps PairHash, which is not a secret).
const (
	MethodLinkSwitchBegin = "devices.link_switch.begin"
	MethodLinkSwitchEnd   = "devices.link_switch.end"
)

var (
	LinkSwitchBeginMethod = buswire.Method[LinkSwitchBegin, LinkSwitchBegun]{Name: MethodLinkSwitchBegin}
	LinkSwitchEndMethod   = buswire.Method[LinkSwitchEnd, buswire.None]{Name: MethodLinkSwitchEnd}
)

// LinkSwitchBegin is devices.link_switch.begin's params. Operation is the manager's id for the switch (the audit's);
// SSHHost is the entry's ssh_host as the manager has it (compare-and-swap); By says who asked (device | admin), for
// the audit.
type LinkSwitchBegin struct {
	MachineID string `json:"machine_id"`
	Operation string `json:"operation"`
	SSHHost   string `json:"ssh_host,omitempty"`
	By        string `json:"by,omitempty"`
}

// LinkSwitchBegun is its answer: the pairing link (persona://link?…, secret) and the sha-256 of its token, hex
// (pair_tokens.token_hash), by which end revokes exactly it.
type LinkSwitchBegun struct {
	Link     string `json:"link"`
	PairHash string `json:"pair_hash"`
}

// LinkSwitchEnd is devices.link_switch.end's params; its answer is {}.
type LinkSwitchEnd struct {
	MachineID string `json:"machine_id"`
	Operation string `json:"operation"`
	PairHash  string `json:"pair_hash,omitempty"`
	Outcome   string `json:"outcome"` // LinkSwitchSucceeded | LinkSwitchRolledBack
}

// LinkSwitchEnd outcomes.
const (
	LinkSwitchSucceeded  = "succeeded"
	LinkSwitchRolledBack = "rolled_back"
)

// Why the hub refused begin or end (rpc.Error codes). The manager shows each as proto.LinkSwitch* reason.
const (
	LinkSwitchCodeNotSSH       = "not_ssh"          // begin: the entry is not kind ssh (end: not kind link)
	LinkSwitchCodeChanged      = "config_changed"   // the entry is not what the manager saw (ssh_host, owner)
	LinkSwitchCodePending      = "pending"          // the user has not confirmed the computer
	LinkSwitchCodeUnavailable  = "link_unavailable" // the hub takes no Persona Link (no TLS port, no https public_url)
	LinkSwitchCodeExistingLink = "existing_link"    // the computer has a Persona Link credential or unused code already
	LinkSwitchCodeNoMachine    = "no_machine"       // the hub has no such machine, or not of this manager
)

// LinkSwitchCodeSucceeded is end(rolled_back)'s answer for an operation the hub saw succeed: nothing is undone, the
// switch stays done.
const LinkSwitchCodeSucceeded = "already_succeeded"

// LinkSwitchCodeRolledBackGone is end(rolled_back)'s answer when the hub has undone the switch (its code and credential
// revoked, the end recorded) but the computer is no longer this manager's (moved or removed since): the rollback is
// taken, and the manager cleans up what it set up on the computer as after {}.
const LinkSwitchCodeRolledBackGone = "rolled_back_gone"
