// Filesystem and event-receipt helpers for the test-only desktop update harness.
import { readFile } from "node:fs/promises";
import { join } from "node:path";

const PRODUCT = "Persona Update Test";
const sleep = milliseconds => new Promise(done => setTimeout(done, milliseconds));

/** electron-builder writes app-update.yml directly to the platform's resources directory. */
export function packagedUpdateConfigPath(buildDirectory, platform, architecture = process.arch) {
  let parts;
  if (platform === "win32") parts = ["win-unpacked", "resources", "app-update.yml"];
  else if (platform === "darwin") parts = [architecture === "x64" ? "mac" : `mac-${architecture}`, `${PRODUCT}.app`, "Contents", "Resources", "app-update.yml"];
  else if (platform === "linux") parts = ["linux-unpacked", "resources", "app-update.yml"];
  else throw new Error(`unsupported update-test platform: ${platform}`);
  return join(buildDirectory, ...parts);
}

export function readPackagedUpdateConfig(buildDirectory, platform, architecture) {
  return readFile(packagedUpdateConfigPath(buildDirectory, platform, architecture), "utf8");
}

export async function readUpdateTestEvents(profile) {
  let text;
  try { text = await readFile(join(profile, "update-test.jsonl"), "utf8"); }
  catch (error) { if (error?.code === "ENOENT") return []; throw error; }
  // A writer can be between lines; retry that partial last line, never a malformed complete record or an I/O failure.
  const lines = text.split("\n");
  if (lines.at(-1) !== "") lines.pop();
  return lines.filter(Boolean).map(line => JSON.parse(line));
}

/** Poll the app's real event file until the predicate returns a value or the deadline passes. */
export async function watchUpdateTestEvents(profile, seconds, predicate, { pollMilliseconds = 500 } = {}) {
  const deadline = Date.now() + seconds * 1000;
  for (;;) {
    const found = predicate(await readUpdateTestEvents(profile));
    if (found) return found;
    if (Date.now() > deadline) return undefined;
    await sleep(pollMilliseconds);
  }
}

export async function finishUpdateTest({ runMain, readEvents, cleanup, report, message }) {
  let result;
  let failure;
  try { result = await runMain(); } catch (error) { failure = message(error); }
  let profileEvents = [];
  let receiptFailure;
  try { profileEvents = await readEvents(); } catch (error) { receiptFailure = `could not read update-test event receipt: ${message(error)}`; }
  let cleanupFailure;
  try { cleanupFailure = await cleanup(); } catch (error) { cleanupFailure = `cleanup failed: ${message(error)}`; }
  const finalFailure = [failure, receiptFailure, cleanupFailure].filter(Boolean).join("; ");
  try {
    await report({ result, failure: finalFailure || undefined, cleanupFailure: cleanupFailure || undefined, profileEvents });
  } catch (error) {
    const reportFailure = `could not write update-test result report: ${message(error)}`;
    return { ok: false, failure: [finalFailure, reportFailure].filter(Boolean).join("; ") };
  }
  return { ok: !finalFailure, failure: finalFailure || undefined };
}

export function isNsisInstallAt(event, expectedExecutableDirectory) {
  return event?.event === "nsis-install" && event.executableDirectory === expectedExecutableDirectory;
}

/**
 * Await the event before reading it. Only the running executable proves its location; the optional updater field
 * remains diagnostic and is deliberately not part of this verdict.
 */
export async function watchForNsisInstall(profile, seconds, expectedExecutableDirectory, options) {
  const event = await watchUpdateTestEvents(profile, seconds, events => events.find(entry => isNsisInstallAt(entry, expectedExecutableDirectory)), options);
  return { event, matchesExpectedDirectory: isNsisInstallAt(event, expectedExecutableDirectory) };
}
