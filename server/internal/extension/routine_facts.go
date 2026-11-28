package extension

import (
	"encoding/json"
	"fmt"
	"reflect"

	"github.com/nwverse/persona/sdk/go/wire"
)

func init() {
	Points["routine_facts"] = Point{
		Name: "routine_facts", Namespace: "routine_facts", Version: 1, NeedsRPC: true,
		Item: func(raw json.RawMessage) (string, error) {
			var item wire.RoutineFactsItem
			if err := strict(raw, &item); err != nil {
				return "", err
			}
			if !ValidID(item.ID.String()) {
				return "", fmt.Errorf("routine_facts: invalid id %q", item.ID)
			}
			if !shortText(item.Title, 80) {
				return "", fmt.Errorf("routine_facts %s: title must have 1 to 80 characters of plain text", item.ID)
			}
			return item.ID.String(), nil
		},
	}
	catalogItemTypes["routine_facts"] = reflect.TypeFor[wire.RoutineFactsItem]()
}

// RoutineFacts lists the briefing sources declared by the extension.
func (m *Manifest) RoutineFacts() []wire.RoutineFactsItem {
	var items []wire.RoutineFactsItem
	_ = json.Unmarshal(m.Contributes["routine_facts"], &items)
	return items
}
