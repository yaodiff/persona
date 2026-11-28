// Secret storage carries public metadata and encrypted bytes only. Values are read by the local CLI.
package wire

import (
	"errors"
	"regexp"
	"slices"
	"strings"
)

const SecretStoreFieldsMost = 16
const SecretStoreValueMost = 16 << 10
const SecretStoreSealedMost = 550 << 10

var storeFieldName = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_. -]{0,79}$`)

// Validate rejects metadata that could be interpreted as op options, paths or hidden value fields.
func (t SecretStoreTarget) Validate() error {
	bad := errors.New("invalid secret store metadata")
	clean := func(s string, most int) bool {
		return s != "" && len(s) <= most && strings.TrimSpace(s) == s && !strings.ContainsAny(s, "\r\n\x00") && !strings.HasPrefix(s, "-")
	}
	if !clean(t.Vault, 120) || (t.Operation != "create" && t.Operation != "edit") || len(t.Fields) < 1 || len(t.Fields) > SecretStoreFieldsMost {
		return bad
	}
	if t.Operation == "create" && (t.Item != "" || !clean(t.Title, 200) || !slices.Contains([]string{"LOGIN", "PASSWORD", "SECURE_NOTE", "API_CREDENTIAL", "SERVER", "DATABASE"}, t.Category)) {
		return bad
	}
	if t.Operation == "edit" && (!clean(t.Item, 120) || t.Category != "" || (t.Title != "" && !clean(t.Title, 200))) {
		return bad
	}
	seen := map[string]bool{}
	for _, f := range t.Fields {
		k := strings.ToLower(f.Name)
		if !storeFieldName.MatchString(f.Name) || strings.TrimSpace(f.Name) != f.Name || seen[k] {
			return bad
		}
		if f.Delete {
			if t.Operation != "edit" || f.Type != "" || f.KeepValue {
				return bad
			}
		} else {
			if f.KeepValue && t.Operation != "edit" {
				return bad
			}
			if f.Type != "STRING" && f.Type != "CONCEALED" && f.Type != "OTP" {
				return bad
			}
		}
		seen[k] = true
	}
	return nil
}
