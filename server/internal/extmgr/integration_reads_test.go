package extmgr

import (
	"errors"
	"testing"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/botport"
	"github.com/nwverse/persona/server/internal/mailwire"
	"github.com/nwverse/persona/server/internal/moduleswitch"
)

type integrationMail struct {
	Mail
	err error
}

func (m integrationMail) ListAccounts() ([]mailwire.MailAccount, error) { return nil, m.err }

func TestIntegrationServiceReadsFollowProviderSwitches(t *testing.T) {
	if !botport.InBuild() {
		t.Skip("Lark is outside this build")
	}
	needsMailModule(t)
	for _, tt := range []struct {
		name     string
		off      []string
		failMail bool
		wantLark assistantwire.IntegrationState
		wantMail assistantwire.IntegrationState
	}{
		{"available providers need setup", nil, false, "needs_setup", "needs_setup"},
		{"mail read failed", nil, true, "needs_setup", "unavailable"},
		{"Lark switched off", []string{"lark"}, false, "module_off", "needs_setup"},
		{"mail switched off", []string{"mail"}, true, "needs_setup", "module_off"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			h := New(Options{Dir: t.TempDir(), Modules: moduleswitch.Off(tt.off...)})
			h.bots.loaded = true
			mail := integrationMail{}
			if tt.failMail {
				mail.err = errors.New("provider diagnostic")
			}
			h.mail = mail
			close(h.started)
			services := h.IntegrationServices(t.Context())
			if len(services) != 2 || services[0].ModuleFeature != "module.lark" || services[0].Availability.State != tt.wantLark ||
				services[1].ModuleFeature != "module.mail" || services[1].Availability.State != tt.wantMail {
				t.Fatalf("services %+v", services)
			}
			if tt.wantMail == assistantwire.IntegrationStateUnavailable &&
				services[1].Availability.Reason != assistantwire.IntegrationReasonReadFailed {
				t.Fatalf("mail failure %+v", services[1])
			}
		})
	}
}
