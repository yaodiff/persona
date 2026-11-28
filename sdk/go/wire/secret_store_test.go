package wire

import "testing"

func TestSecretStoreTargetValidateOTPDeleteAndKeepValue(t *testing.T) {
	valid := []SecretStoreTarget{
		{
			Operation: "create",
			Vault:     "Dev",
			Title:     "Synthetic login",
			Category:  "LOGIN",
			Fields: []SecretStoreField{
				{Name: "username", Type: "STRING"},
				{Name: "password", Type: "CONCEALED"},
				{Name: "totp_secret", Type: "OTP"},
			},
		},
		{
			Operation: "edit",
			Vault:     "vaultid123",
			Item:      "itemid123",
			Fields: []SecretStoreField{
				{Name: "totp_secret", Type: "OTP", KeepValue: true},
				{Name: "duplicate_field", Delete: true},
			},
		},
	}
	for i, target := range valid {
		if err := target.Validate(); err != nil {
			t.Fatalf("valid case %d failed: %v", i, err)
		}
	}

	invalid := []SecretStoreTarget{
		// Unsupported field type.
		{
			Operation: "create",
			Vault:     "Dev",
			Title:     "Synthetic",
			Category:  "LOGIN",
			Fields:    []SecretStoreField{{Name: "totp", Type: "TOTP"}},
		},
		// Delete on create.
		{
			Operation: "create",
			Vault:     "Dev",
			Title:     "Synthetic",
			Category:  "LOGIN",
			Fields:    []SecretStoreField{{Name: "old", Delete: true}},
		},
		// Delete with type.
		{
			Operation: "edit",
			Vault:     "vaultid123",
			Item:      "itemid123",
			Fields:    []SecretStoreField{{Name: "old", Type: "STRING", Delete: true}},
		},
		// Delete with keep_value.
		{
			Operation: "edit",
			Vault:     "vaultid123",
			Item:      "itemid123",
			Fields:    []SecretStoreField{{Name: "old", Delete: true, KeepValue: true}},
		},
		// KeepValue on create.
		{
			Operation: "create",
			Vault:     "Dev",
			Title:     "Synthetic",
			Category:  "LOGIN",
			Fields:    []SecretStoreField{{Name: "totp", Type: "OTP", KeepValue: true}},
		},
		// KeepValue without type.
		{
			Operation: "edit",
			Vault:     "vaultid123",
			Item:      "itemid123",
			Fields:    []SecretStoreField{{Name: "totp", KeepValue: true}},
		},
		// Duplicate field names (case-insensitive).
		{
			Operation: "edit",
			Vault:     "vaultid123",
			Item:      "itemid123",
			Fields: []SecretStoreField{
				{Name: "totp_secret", Type: "OTP", KeepValue: true},
				{Name: "TOTP_SECRET", Delete: true},
			},
		},
	}
	for i, target := range invalid {
		if err := target.Validate(); err == nil {
			t.Fatalf("invalid case %d unexpectedly passed", i)
		}
	}
}
