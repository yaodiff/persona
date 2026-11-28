// Package archguard holds only tests that read the server's source: they pin the import boundaries between the
// contract packages and the parts (the hub, the manager, the node), docs/server/architecture.md "Contract packages",
// where the entry points reap inherited children (osproc.ReapInherited), ahead of any child they start, and that every
// listener comes from osproc.Listen or osproc.FileListener, so no child inherits a connection on macOS. They also
// freeze the names of the central lists a domain registers around (docs/server/architecture.md "Registering a
// domain's entries"), and keep the bus methods the assistant kernel's modules answer off the central dispatch
// (kernel_bus_test.go; "Validated and sealed").
package archguard
