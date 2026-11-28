package assistant

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/proto"
)

// trackLaunchers answers GET /api/v1/assistant/tracks/launchers: a narrow, read-only list of healthy launchers
// available on the assistant's machine for starting a space conversation, plus the global default.
// Granted to extensions with the spaces scope so space-conversations can offer launcher/model selection before start.
func (s *Service) trackLaunchers(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	out := assistantwire.TrackLaunchers{Launchers: []assistantwire.TrackLauncher{}}
	mid := s.settings(ctx).MachineID
	d, ok := s.device(ctx, mid)
	if !ok {
		apierr.WriteJSON(w, out)
		return
	}
	if !d.ExternalInventory {
		ctx = context.WithValue(ctx, pickerDiscoveryDeadline{}, time.Now().Add(launcherSyncTimeout+time.Second))
	}
	all, list, _, err := s.assistantPickerLaunchers(ctx, d, false, s.requestLang(r))
	if err != nil {
		apierr.WriteJSON(w, out)
		return
	}
	def := s.pickerDefault(ctx, r, all)
	defLauncher, _ := def["provider"].(string)
	defModel, _ := def["model"].(string)
	defEffort, _ := def["effort"].(string)
	out.Default = assistantwire.TrackLauncherDefault{
		Launcher: strings.TrimSpace(defLauncher),
		Model:    strings.TrimSpace(defModel),
		Effort:   strings.TrimSpace(defEffort),
	}
	shown, _ := s.withHealth(ctx, mid, list, defLauncher)
	stored := s.storedLaunchers(ctx)
	for _, l := range shown {
		if proto.IsExternalLauncher(l.Name) || l.Source == proto.ExternalLauncherSource {
			continue
		}
		display := l.DisplayName
		if j := slices.IndexFunc(stored, func(m storedLauncher) bool { return m.Name == l.Name }); j >= 0 && proto.CanonicalLauncherSource(l.Source) == "persona" && stored[j].DisplayName != "" {
			display = stored[j].DisplayName
		}
		var models []string
		if m := strings.TrimSpace(l.Model); m != "" {
			models = append(models, m)
		}
		out.Launchers = append(out.Launchers, assistantwire.TrackLauncher{
			Name:         l.Name,
			DisplayName:  display,
			Agent:        l.AgentType,
			DefaultModel: strings.TrimSpace(l.Model),
			Models:       models,
		})
	}
	apierr.WriteJSON(w, out)
}
