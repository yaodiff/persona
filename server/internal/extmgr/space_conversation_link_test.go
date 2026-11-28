package extmgr

import (
	"testing"

	"github.com/nwverse/persona/server/internal/extension"
)

func TestSpaceConversationsMayOpenOnlyTheConversationHandoff(t *testing.T) {
	page := extension.UIItem{ID: "space", Placement: "extension_page"}
	ok := "persona://tracks/computer-example/reading/session-example"
	if err := checkOpenURL(spacesMigrationID, nil, page, "", ok); err != nil {
		t.Fatalf("%s: %v", ok, err)
	}
	if err := checkOpenURL("another-extension", nil, page, "", ok); err == nil {
		t.Fatal("another extension opened the conversation handoff")
	}
	for _, bad := range []string{
		"persona://tracks/computer-example/health/session-example",
		"persona://tracks/computer-example/reading",
		"persona://tracks/computer-example/reading/session-example/more",
		"persona://tracks/computer-example//session-example",
		"persona://tracks/computer-example/reading/session%2Fexample",
		"persona://tracks/computer-example/reading/session example",
		"persona://tracks/computer-example/reading/session-example?x=1",
		"persona://tracks/computer-example/reading/session-example?",
		"persona://tracks/computer-example/reading/session-example#x",
		"persona://user@tracks/computer-example/reading/session-example",
		"persona://inbox/computer-example/reading/session-example",
		"persona://settings",
		"javascript:alert(1)",
	} {
		if err := checkOpenURL(spacesMigrationID, nil, page, "", bad); err == nil {
			t.Errorf("%s: allowed", bad)
		}
	}
}
