-- Private market keys (docs/extensions/extensions.md "Private market keys"): the Ed25519 public keys ("ed25519:<base64>")
-- the owner trusts for a nonofficial market, as a JSON array. Empty, the default for every existing market, keeps it
-- unsigned exactly as before; with keys, its catalog must be signed by one of them. The official market never has any.
ALTER TABLE plugin_markets ADD COLUMN IF NOT EXISTS trusted_keys jsonb NOT NULL DEFAULT '[]'::jsonb;
ALTER TABLE plugin_markets ADD CONSTRAINT plugin_markets_trusted_keys_shape
	CHECK (jsonb_typeof(trusted_keys) = 'array' AND jsonb_array_length(trusted_keys) <= 8);
