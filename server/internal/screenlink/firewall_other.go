//go:build !darwin

package screenlink

// firewallBlocks is false where there is no firewall the node can read: a blocked port only fails the phone's probe.
func firewallBlocks() bool { return false }
