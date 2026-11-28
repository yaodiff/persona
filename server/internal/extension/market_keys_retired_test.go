package extension

import "testing"

// A retired official key (Official.PreviousSigningKeys) is refused for a private market like the current one (#16254):
// a module's flag trusts packages verified with either, so a private market must never record one. Every spelling of
// the key (whitespace, the same key among others) is refused, and the market keeps no key from the list.
func TestNormalizeMarketKeysRejectsRetiredOfficial(t *testing.T) {
	if len(Official.PreviousSigningKeys) == 0 {
		t.Fatal("the official market has no retired key to test with")
	}
	_, own := newPrivateKey(t)
	_, refused := NormalizeMarketKeys([]string{Official.SigningKey})
	if refused == nil {
		t.Fatal("the current official key was taken")
	}
	for _, retired := range Official.PreviousSigningKeys {
		for name, keys := range map[string][]string{
			"alone":           {retired},
			"padded":          {"  " + retired + " "},
			"after its own":   {own, retired},
			"official before": {Official.SigningKey, retired},
		} {
			got, err := NormalizeMarketKeys(keys)
			if err == nil || err.Error() != refused.Error() {
				t.Errorf("%s: %v taken (%v, error %v)", name, keys, got, err)
			}
			if got != nil {
				t.Errorf("%s: kept %v", name, got)
			}
		}
	}
	// A test market whose official key list is replaced still refuses only those keys.
	saved := Official
	t.Cleanup(func() { Official = saved })
	_, current := newPrivateKey(t)
	_, previous := newPrivateKey(t)
	Official = OfficialMarket{Spec: saved.Spec, SigningKey: current, PreviousSigningKeys: []string{previous}}
	if _, err := NormalizeMarketKeys([]string{previous}); err == nil {
		t.Fatal("the replaced retired key was taken")
	}
	if got, err := NormalizeMarketKeys([]string{saved.PreviousSigningKeys[0]}); err != nil || len(got) != 1 {
		t.Fatalf("a key that is no longer official was refused: %v %v", got, err)
	}
}
