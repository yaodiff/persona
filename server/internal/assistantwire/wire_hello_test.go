package assistantwire

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestHelloPushGradingCardsJSON(t *testing.T) {
	rawOff, err := json.Marshal(Hello{Locale: "en"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(rawOff), "push_grading_cards") {
		t.Fatalf("expected push_grading_cards omitted when nil, got %s", rawOff)
	}

	want := &PushGradingCards{
		SupervisorHold:  false,
		EscalateReasons: []string{"irreversible"},
		HoldConclusions: false,
		HoldAcceptance:  true,
		DecisionLevel:   LevelP1,
		ReportLevel:     LevelP2,
	}
	rawOn, err := json.Marshal(Hello{Locale: "en", QuietPush: true, PushGradingCards: want})
	if err != nil {
		t.Fatal(err)
	}
	var decoded Hello
	if err := json.Unmarshal(rawOn, &decoded); err != nil {
		t.Fatal(err)
	}
	if !decoded.QuietPush || !reflect.DeepEqual(decoded.PushGradingCards, want) {
		t.Fatalf("decoded = %+v, want QuietPush=true cards=%+v", decoded, want)
	}
}
