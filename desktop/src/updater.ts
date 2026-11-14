import { app, autoUpdater as nativeUpdater } from "electron";
import electronUpdater from "electron-updater";
import { UpdateController, updatesSupported } from "./update-state.js";
import type { UpdateState } from "./update-state.js";
const { autoUpdater } = electronUpdater;

/**
 * Public GitHub releases only; no repository token is embedded in the client.
 *
 * The feed comes from the `app-update.yml` the package was built with. Nothing at run time (an environment variable,
 * a setting or the hub's page) can point a packaged app at another feed; the one switch here only turns checks off.
 *
 * Returns nothing where updates do not exist (development runs, Linux), so the tray shows no update entries there.
 */
export function installAutoUpdate(deps: {
  /** Called on every state change; the tray redraws its update section from it. */
  readonly changed: (state: UpdateState) => void;
  /** Lets windows close instead of hiding to the tray, immediately before the app quits to install. */
  readonly prepareQuit: () => void;
}): UpdateController | undefined {
  if (!updatesSupported(app.isPackaged, process.platform, process.env.PERSONA_DESKTOP_DISABLE_UPDATE === "1")) return undefined;
  autoUpdater.autoDownload = true;
  // Quitting installs a downloaded update; while the app runs, only "Restart to update" does.
  autoUpdater.autoInstallOnAppQuit = true;
  const controller = new UpdateController({
    check: () => autoUpdater.checkForUpdates(),
    install: () => {
      deps.prepareQuit();
      // Silent, then reopen: the person asked for a restart, not for an installer to click through.
      autoUpdater.quitAndInstall(true, true);
    },
    setTimer: (run, milliseconds) => setTimeout(run, milliseconds),
    clearTimer: handle => clearTimeout(handle as ReturnType<typeof setTimeout>),
    changed: deps.changed,
  });
  autoUpdater.on("update-available", info => controller.dispatch({ type: "available", version: info.version }));
  autoUpdater.on("update-not-available", () => controller.dispatch({ type: "current" }));
  if (process.platform === "darwin") {
    // electron-updater emits update-downloaded when it hands the file to Squirrel.Mac, before the signature and
    // install location are accepted. Only Squirrel's own verdict makes Restart to update actionable; a refusal
    // arrives as an error and returns the controller to the retry schedule.
    nativeUpdater.once("update-downloaded", () => controller.dispatch({ type: "downloaded" }));
  } else {
    autoUpdater.on("update-downloaded", () => controller.dispatch({ type: "downloaded" }));
  }
  autoUpdater.on("error", error => {
    // The web UI stays available; the next scheduled check retries. Only a check the person asked for shows the failure.
    console.warn("Desktop update failed:", error.message);
    controller.dispatch({ type: "failed" });
  });
  controller.start();
  return controller;
}
