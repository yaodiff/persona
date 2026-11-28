package prompts

import (
	"encoding/json"

	"github.com/nwverse/persona/server/internal/modapi"
)

// RenderRoutineContributions adds a bounded block of quoted provider data to a routine's
// host-written facts. JSON quoting keeps line breaks, quotes and XML delimiters inside data.
func RenderRoutineContributions(base, lang string, contributions []modapi.RoutineContribution) string {
	if len(contributions) == 0 {
		return base
	}
	data, err := json.Marshal(contributions)
	if err != nil {
		panic(err) // RoutineContribution contains only strings and slices.
	}
	return render("assistant/routine_contributions", struct{ Base, Lang, Data string }{base, lang, string(data)})
}
