// Package seam is the assistant's typed registration seam: the modules inside the manager's tree register through it
// while assistant.New runs, and New seals it before it returns. It takes typed ports from package assistant and names
// that their own constructors validate, so a registration mistake is a defect raised at the line that made it, while
// New runs, never a wrong route or a nil branch found by a request (docs/server/architecture.md "Registering a
// domain's entries", "Validated and sealed"). Route handlers registered here run under the kernel's one catch site,
// dispatch.go; bus methods (bus.go) and hooks (hooks.go) run where the central lines they replaced ran.
//
// It sits under package kernel, which hosts the built-in feature modules (internal/modapi). Those can be left out or
// switched off; the modules registered here are part of Persona Core and always register.
//
//persona:layer application seam
package seam
