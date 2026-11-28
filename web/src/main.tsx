// The modules with a page of their own (the live screen, /screen/<machine>): never the whole list (it says why).
import { standalonePage } from "./modules/standalone";
import { takeImportLinkOnStart } from "./lib/import-link";
import { initI18n, startI18n } from "./i18n";
import "./styles.css";
import { applyLook } from "./ui/look";
import { logCaughtError } from "./lib/caught-error";

// A provider import link (#5420) may hold a key: out of the address bar before the page waits for anything, the
// language's strings or App (lib/import-link.ts). Any other fragment stays, so a sign-in link's `#token=` still signs in.
takeImportLinkOnStart();

// Theme color and text size (docs/web/web.md "Theme color and text size") before anything draws.
applyLook();

// The language's catalog loads before the rest of the app: their modules call t() as they load (docs/web/web.md "Internationalization").
// The build waits only for the first screen's part of it, as its lazy imports wait for the rest; the dev server's do not.
// React and the PWA helper say no t(), so they load alongside it rather than after it.
// An overlay's history entry a reload left behind goes before the app reads the address (lib/back-close.ts, #5134).
const [, { StrictMode, createElement }, { createRoot }, { startPWA }] = await Promise.all([
  import.meta.env.DEV ? initI18n() : startI18n(),
  import("react"),
  import("react-dom/client"),
  import("./lib/pwa"),
  import("./lib/back-close").then((m) => m.settleOverlayEntry()),
]);

startPWA();

// A module's page of its own (the live screen): no sign-in flow, no sidebar, and its code loads only there. While its
// module is off the address is an unknown one, and its fragment (a token the app meant for that page) goes before App
// could take it for a sign-in (modules/standalone.ts).
const page = await standalonePage(window.location.href);
// a crash a boundary catches is logged with its component stack (lib/caught-error.ts)
const root = createRoot(document.getElementById("root")!, { onCaughtError: logCaughtError });
if (page) {
  root.render(createElement(StrictMode, null, page));
} else if (window.location.pathname === "/login/device") {
  const { NativePasswordLogin } = await import("./views/PasswordLogin");
  root.render(createElement(StrictMode, null, createElement(NativePasswordLogin)));
} else {
  // Error reports (docs/web/web.md "Error reports") start with the app: they go out only if the hub's hello has the feature.
  const [{ ExtensionThemeProvider }, { App }] = await Promise.all([import("./ui/ExtensionThemePicker"), import("./App"), import("./lib/error-reports-start")]);
  root.render(createElement(StrictMode, null, createElement(ExtensionThemeProvider, null, createElement(App))));
  await import("./lib/app-after-render");
}
