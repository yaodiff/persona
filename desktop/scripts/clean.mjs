import { rm } from "node:fs/promises";

// TypeScript does not remove tests or assets for a deleted source; stale output must not look like current evidence.
await Promise.all(["dist", "dist-preload", "dist-test"].map(path => rm(path, { recursive: true, force: true })));
