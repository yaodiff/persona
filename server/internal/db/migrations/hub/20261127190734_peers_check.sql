-- SavePeer, UpdatePeerSeen and RedeemPeerCode marshal nonNil(endpoints) as an array of strings.
ALTER TABLE peers ADD CONSTRAINT peers_endpoints_shape
    CHECK (jsonb_typeof(endpoints) = 'array') NOT VALID;
