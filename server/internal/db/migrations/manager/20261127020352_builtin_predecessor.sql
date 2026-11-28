-- Keep a verified built-in predecessor distinguishable from an ordinary installation after a restart.
ALTER TABLE plugins ADD COLUMN builtin_predecessor boolean NOT NULL DEFAULT false;
