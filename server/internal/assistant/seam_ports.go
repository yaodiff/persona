package assistant

import (
	"net/http"
	"runtime/debug"

	"github.com/nwverse/persona/server/internal/assistant/kernel/seam"
	"github.com/nwverse/persona/server/internal/domainkit/defect"
)

// The ports the kernel's registry takes from the Service (kernel/seam; docs/server/architecture.md "Validated and
// sealed"). New makes the registry with newRegistry, the modules register through it, and New returns it sealed.

// newRegistry is the registry the Service's modules register through, kept as s.kernel: the Service answers their
// bus methods and runs their hooks through it (seam_slots.go).
func (s *Service) newRegistry() *seam.Registry {
	s.kernel = seam.NewRegistry(seamRoutes{s}, seamAccess{}, seamBodies{}, seamDefects{s})
	return s.kernel
}

// seamRoutes serves a module's routes the way New's own route calls are served (Service.serveRoute).
type seamRoutes struct{ s *Service }

func (t seamRoutes) Install(r seam.Route) { r.Accept(t) }

func (t seamRoutes) VisitRoute(pattern string, serve http.HandlerFunc) {
	t.s.serveRoute(pattern, serve)
}

// seamAccess is fullRole: a device that may do everything (RoleHeader).
type seamAccess struct{}

func (seamAccess) Full(r *http.Request) bool { return fullRole(r) }

// seamBodies is writeBodyErr: 413 for a body over the limit, else 400.
type seamBodies struct{}

func (seamBodies) Write(w http.ResponseWriter, err, bad error) { writeBodyErr(w, err, bad) }

// seamDefects logs a defect a module's route raised, with its stack, before the kernel answers it with 500.
type seamDefects struct{ s *Service }

func (d seamDefects) Report(r *http.Request, err defect.Defect) {
	d.s.logf("assistant: defect on %s: %v\n%s", r.Pattern, err, debug.Stack())
}
