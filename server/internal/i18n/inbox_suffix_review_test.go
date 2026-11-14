package i18n

import (
	"fmt"
	"maps"
	"slices"
	"strings"
	"testing"
)

var (
	inboxSuffixReviewLangs = []Lang{En, ZhHans, Ja}
	inboxPluralSuffixes    = []string{"_zero", "_one", "_two", "_few", "_many", "_other"}
)

// InboxText re-finds a text's key in the catalog and drops the key's plural suffix to get the base it looks up
// (makeCatalogTemplates). A key whose name merely ends in _zero, _one, _two, _few, _many or _other is therefore
// looked up as a stem that is not a key, and the text reads back as that stem (#6955: inbox-errors.merge_few).
// A suffix is a genuine plural form when any language has the base key or the family has at least two distinct forms
// across the three languages. Corresponding forms in the other languages are then valid too. The text stays as it is.
func TestInboxTextKeysCarryNoBarePluralSuffix(t *testing.T) {
	for _, violation := range barePluralSuffixViolations(catalogs(), "inbox-errors.", "assistant-inbox_chain.") {
		t.Error(violation)
	}
}

func barePluralSuffixViolations(cats map[Lang]map[string]string, namespaces ...string) []string {
	type form struct {
		lang   Lang
		key    string
		suffix string
		text   string
	}

	families := map[string][]form{}
	for _, lang := range inboxSuffixReviewLangs {
		cat := cats[lang]
		for _, key := range slices.Sorted(maps.Keys(cat)) {
			eligible := false
			for _, namespace := range namespaces {
				if strings.HasPrefix(key, namespace) {
					eligible = true
					break
				}
			}
			if !eligible {
				continue
			}
			stem, suffix := inboxPluralStem(key)
			if suffix == "" {
				continue
			}
			families[stem] = append(families[stem], form{lang: lang, key: key, suffix: suffix, text: cat[key]})
		}
	}

	var violations []string
	for _, stem := range slices.Sorted(maps.Keys(families)) {
		forms := families[stem]
		hasBase := false
		for _, lang := range inboxSuffixReviewLangs {
			if _, ok := cats[lang][stem]; ok {
				hasBase = true
				break
			}
		}
		distinctForms := map[string]bool{}
		for _, form := range forms {
			distinctForms[form.suffix] = true
		}
		if hasBase || len(distinctForms) >= 2 {
			continue
		}
		for _, form := range forms {
			violations = append(violations, fmt.Sprintf(
				"%s: %s reads back as %q, not %q: its name ends in a plural suffix and %s is not a key; rename the key",
				form.lang, form.key, stem, form.text, stem))
		}
	}
	return violations
}

func inboxPluralStem(key string) (stem, suffix string) {
	for _, suffix := range inboxPluralSuffixes {
		if strings.HasSuffix(key, suffix) {
			return strings.TrimSuffix(key, suffix), suffix
		}
	}
	return key, ""
}

func fakeIsolatedPluralCatalogs() map[Lang]map[string]string {
	const key = "assistant-inbox_chain.foo_other"
	return map[Lang]map[string]string{
		En:     {key: "fake"},
		ZhHans: {key: "fake"},
		Ja:     {key: "fake"},
	}
}

func TestBarePluralSuffixViolationsFindIsolatedForm(t *testing.T) {
	got := barePluralSuffixViolations(fakeIsolatedPluralCatalogs(), "assistant-inbox_chain.")
	want := []string{
		`en: assistant-inbox_chain.foo_other reads back as "assistant-inbox_chain.foo", not "fake": its name ends in a plural suffix and assistant-inbox_chain.foo is not a key; rename the key`,
		`zh-Hans: assistant-inbox_chain.foo_other reads back as "assistant-inbox_chain.foo", not "fake": its name ends in a plural suffix and assistant-inbox_chain.foo is not a key; rename the key`,
		`ja: assistant-inbox_chain.foo_other reads back as "assistant-inbox_chain.foo", not "fake": its name ends in a plural suffix and assistant-inbox_chain.foo is not a key; rename the key`,
	}
	if !slices.Equal(got, want) {
		t.Fatalf("violations:\n got: %q\nwant: %q", got, want)
	}
}

func TestBarePluralSuffixReviewAllowsCrossLanguageFamilies(t *testing.T) {
	tests := []struct {
		name string
		add  func(map[Lang]map[string]string)
	}{
		{
			name: "base in another language",
			add: func(cats map[Lang]map[string]string) {
				cats[En]["assistant-inbox_chain.foo"] = "base"
			},
		},
		{
			name: "second form in another language",
			add: func(cats map[Lang]map[string]string) {
				cats[En]["assistant-inbox_chain.foo_one"] = "one"
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cats := fakeIsolatedPluralCatalogs()
			test.add(cats)
			if got := barePluralSuffixViolations(cats, "assistant-inbox_chain."); len(got) != 0 {
				t.Fatalf("cross-language plural family reported as bare: %q", got)
			}
		})
	}
}
