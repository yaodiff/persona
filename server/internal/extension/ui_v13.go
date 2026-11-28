package extension

// UI version 13 (docs/extensions/extensions.md "UI version 13"): a code's navigate, the action an app calls when a name
// in the code is chosen to follow it (go to definition, find usages). downgrade13 leaves it out for an older app and a
// native stream, whose vocabulary has no navigate: the code stays as it was, and the extension's own rows (an
// outline, a usages list) do what the click would have.

// downgrade13 takes every code's navigate out, in place.
func downgrade13(v *View) {
	for i := range v.Sections {
		for j := range v.Sections[i].Items {
			v.Sections[i].Items[j].Navigate = nil
		}
	}
}
