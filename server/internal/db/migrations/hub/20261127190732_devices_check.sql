-- Pusher.setPrefsBy marshals NotifyPrefs as an object. Device registration may leave prefs unset.
ALTER TABLE devices ADD CONSTRAINT devices_prefs_shape
    CHECK (prefs IS NULL OR jsonb_typeof(prefs) = 'object') NOT VALID;
