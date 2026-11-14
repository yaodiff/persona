// Entry of the test-only update build (electron-builder.update-test.yml). A release package never contains this file.
//
// It observes the real updater and the real main process; it does not replace either. The feed address is the one
// baked into this build's app-update.yml.
import { appendFileSync, mkdirSync } from "node:fs";
import { dirname, join } from "node:path";
import { app, autoUpdater as nativeUpdater } from "electron";
import electronUpdater from "electron-updater";

const { autoUpdater } = electronUpdater;
const directory = app.getPath("userData");
mkdirSync(directory, { recursive: true });
const record = entry => appendFileSync(join(directory, "update-test.jsonl"), `${JSON.stringify({ at: Date.now(), ...entry })}\n`);

// A test build must not take over the persona:// links of a real install on the same computer.
app.isDefaultProtocolClient = () => true;
app.setAsDefaultProtocolClient = () => false;

// Keep the production updater implementation and its full argument list, but expose its NSIS install operation.
// The real executable path is receipt evidence. electron-updater's optional installDirectory is diagnostic only:
// locked NsisUpdater does not assign it, and it does not report the destination NSIS chooses through /D or its GUID key.
if (process.platform === "win32") {
  const install = autoUpdater.install.bind(autoUpdater);
  autoUpdater.install = (...args) => {
    const [options] = args;
    const executable = app.getPath("exe");
    const executableDirectory = dirname(executable);
    record({ event: "nsis-install", options, executable, executableDirectory, updaterInstallDirectory: autoUpdater.installDirectory ?? null, executableInRunDirectory: process.env.PERSONA_UPDATE_TEST_EXECUTABLE_DIR === undefined ? null : executableDirectory === process.env.PERSONA_UPDATE_TEST_EXECUTABLE_DIR, forwardedArguments: args.map(argument => typeof argument === "boolean" ? argument : typeof argument) });
    return install(...args);
  };
}

record({ event: "start", version: app.getVersion(), platform: process.platform, packaged: app.isPackaged });
autoUpdater.on("checking-for-update", () => record({ event: "checking" }));
autoUpdater.on("update-available", info => record({ event: "available", version: info.version }));
autoUpdater.on("update-not-available", info => record({ event: "current", version: info.version }));
autoUpdater.on("error", error => record({ event: "error", message: String(error?.message ?? error).slice(0, 2000) }));
// The same path as the tray's Quit: the downloaded update installs on the way out.
const quitSoon = () => setTimeout(() => { record({ event: "quit" }); app.quit(); }, 3000);
autoUpdater.on("update-downloaded", info => {
  record({ event: "downloaded", version: info.version, file: info.downloadedFile });
  if (process.platform !== "darwin") quitSoon();
});
// On macOS electron-updater's event only means the file was handed to Squirrel.Mac. Squirrel's own event says it
// accepted the file; a refusal arrives through the error event above, and then nothing is installed.
if (process.platform === "darwin") {
  nativeUpdater.on("update-downloaded", () => { record({ event: "native-downloaded" }); quitSoon(); });
}

await import("../dist/main.js");

// The product ships no Linux package, so its updater stays off there. Checking from here lets a Linux computer
// exercise the same feed, download, checksum and install-on-quit path through the packaged AppImage.
if (process.platform === "linux") {
  void app.whenReady().then(() => {
    autoUpdater.autoDownload = true;
    autoUpdater.autoInstallOnAppQuit = true;
    return autoUpdater.checkForUpdates();
  }).catch(error => record({ event: "error", message: String(error?.message ?? error).slice(0, 2000) }));
}
