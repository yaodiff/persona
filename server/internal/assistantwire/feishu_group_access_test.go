package assistantwire

import "testing"

func TestFeishuGroupAccessMode(t *testing.T) {
	for _, m := range []FeishuGroupAccessMode{FeishuGroupOwnerOnly, FeishuGroupAllowlist, FeishuGroupEveryoneInAllowedChats} {
		if !m.Valid() || m.Effective() != m {
			t.Errorf("%q valid %v effective %q", m, m.Valid(), m.Effective())
		}
	}
	for _, m := range []FeishuGroupAccessMode{"", "everyone", "Owner_Only"} {
		if m.Valid() {
			t.Errorf("%q should not be valid", m)
		}
		if m.Effective() != FeishuGroupOwnerOnly {
			t.Errorf("%q effective %q", m, m.Effective())
		}
	}
}
