package assistant

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/nwverse/persona/server/internal/apierr"
)

// startPresetRoutes: GET /api/v1/assistant/worker-presets?machine=<id> lists the Agent presets (the active model
// profile's tiers) a task can start with on that computer: the presets whose launcher the machine has, each with its
// launcher, model, effort, description and whether its launcher is unavailable now. An extension granted the
// assistant.start_defaults hub scope reads it to name the presets of its start defaults; the list is read-only and never
// carries credentials.
func (s *Service) startPresetRoutes() {
	s.handle("GET /api/v1/assistant/worker-presets", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		machine := r.URL.Query().Get("machine")
		m, ok := s.machineByName(ctx, machine)
		if !ok {
			apierr.Write(w, http.StatusNotFound, errUnknownMachine)
			return
		}
		out, err := s.machinePresets(ctx, m.ID, "") // by id: the rest of the manager addresses machines so, not by name or ssh alias
		if err != nil {
			apierr.Write(w, http.StatusBadGateway, err)
			return
		}
		raw, _ := json.Marshal(map[string]any{"presets": out})
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = w.Write(raw)
	})
}

// machinePresets are the Agent presets machine (an id) can start in host ("": its own launcher list): the profile in
// use's tiers whose launcher it has (pickerTiers), each marked unavailable while its launcher's latest check says it
// does not work. GET /api/v1/assistant/worker-presets lists them, and Agents read them before starting one
// (presetsOn: dispatch_read's overview, a2a_sessions).
func (s *Service) machinePresets(ctx context.Context, machine, host string) ([]workerPreset, error) {
	launchers, err := s.pickerLaunchersOn(ctx, machine, host)
	if err != nil {
		return nil, err
	}
	_, tiers := s.pickerTiers(ctx, machine, launchers)
	out := make([]workerPreset, 0, len(tiers))
	for _, t := range tiers {
		_, unavailable := s.launcherUnusable(ctx, machine, t.Launcher)
		out = append(out, workerPreset{
			Name: t.Name, Launcher: t.Launcher, Model: t.Model, Effort: t.Effort, Speed: t.Speed,
			Description: t.Description, Unavailable: unavailable,
		})
	}
	return out, nil
}

// workerPreset is one row of GET /api/v1/assistant/worker-presets.
type workerPreset struct {
	Name        string `json:"name"`
	Launcher    string `json:"launcher"`
	Model       string `json:"model,omitempty"`
	Effort      string `json:"effort,omitempty"`
	Speed       string `json:"speed,omitempty"`
	Description string `json:"description,omitempty"`
	// Unavailable: the launcher's latest check says it does not work right now (for example a usage limit with a
	// reset still ahead); absent check results leave it false, so an unchecked preset is offered.
	Unavailable bool `json:"unavailable,omitempty"`
}
