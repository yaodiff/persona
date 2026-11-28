//go:build !darwin

package startdir

// answers is nil off macOS: Go's os package asks for no folder while it starts on Linux or Windows, so a folder that
// does not answer cannot hold a command before main there, and this package does nothing.
var answers func(path string) bool
