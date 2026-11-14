package screen

import (
	"slices"
	"strings"
	"testing"
)

// Claude Code's Bypass Permissions confirmation on a fresh start sits at the top of a tall pane, blank rows under it
// down to the bottom. Those rows used to push it out of the rows the readers look back over, so its card had no
// choices and its head was the cursor's row ("❯ No, exit"). It reads as the two choices, each with its keys, under
// its warning.
func TestBypassConfirmationOnATallPaneIsADialog(t *testing.T) {
	plain := fixture(t, "claude-2.1.322-bypass-confirm-160.txt")
	d := ParseDialog(plain)
	if d == nil {
		t.Fatal("no dialog read")
	}
	if len(d.Options) != 2 {
		t.Fatalf("options %+v", d.Options)
	}
	if o := d.Options[0]; o.Label != "No, exit" || !slices.Equal(o.Keys, []string{"enter"}) {
		t.Errorf("first option %+v", o)
	}
	if o := d.Options[1]; o.Label != "Yes, I accept" || !slices.Equal(o.Keys, []string{"down", "enter"}) {
		t.Errorf("second option %+v", o)
	}
	if !strings.Contains(d.Question+" "+d.Header+" "+d.Detail, "Bypass Permissions mode") {
		t.Errorf("the warning is not on the card: %+v", d)
	}
	if got := DialogHead(plain); got != "WARNING: Claude Code running in Bypass Permissions mode" {
		t.Errorf("head %q", got)
	}
	if code, _ := CheckClaude(plain); code != DialogOpen {
		t.Errorf("CheckClaude %q", code)
	}
}

// Blank rows under a dialog change nothing about it: the same options, head and revision as without them.
func TestBlankRowsUnderADialogChangeNothing(t *testing.T) {
	for _, name := range []string{"claude-2.1.316-danger-placeholder-100.txt", "claude-2.1.312-model-picker-162.txt",
		"claude-2.1.322-bypass-confirm-160.txt"} {
		plain := strings.TrimRight(fixture(t, name), "\n ")
		tall := plain + strings.Repeat("\n", 45)
		a, b := ParseDialog(plain), ParseDialog(tall)
		if a == nil || b == nil {
			t.Fatalf("%s: dialog %v / %v", name, a, b)
		}
		if a.Rev != b.Rev || len(a.Options) != len(b.Options) || DialogHead(plain) != DialogHead(tall) {
			t.Errorf("%s: %+v\nvs\n%+v", name, a, b)
		}
	}
}
