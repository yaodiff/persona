// Another tab of this browser picked a language (Settings › General › Appearance › Language, kept as `persona.language`;
// docs/web/web.md "Internationalization"): this page follows it with a reload, as the tab that picked it did. Never over
// something that would be lost: the reload waits for the calm a new build waits for (lib/new-version.ts mayReload: no
// draft, no open dialog or menu, and the page in the background or left alone). Until then a toast that stays says so,
// with a button that reloads at once, and the page looks again when it goes to the background and every few seconds.
import { currentLanguage, i18nRest, preferredLanguage, t } from "../i18n";
import { toast } from "../ui/atoms";
import { mayReload, pageCalm, quietMs, WAIT_TICK_MS, type Calm } from "./new-version";

/** What the follower needs from the world; tests swap it. */
export interface LanguageFollowDeps {
  /** Whether the language this browser keeps now is another than the one this page shows. */
  differs: () => boolean;
  calm: () => Calm;
  reload: () => void;
  notify: (reloadNow: () => void) => void;
}

/** One page's follower: `changed` when another tab saved a language, `tick` to look again until the page reloads. */
export function languageFollower(deps: LanguageFollowDeps) {
  let asked = false;
  let told = false;
  let gone = false;
  const reloadNow = () => {
    if (gone) return;
    gone = true;
    deps.reload();
  };
  const tick = () => {
    // only for another tab's choice (not a system language that changed meanwhile), and while it still differs
    if (!asked || gone || !deps.differs()) return;
    if (mayReload(deps.calm())) return reloadNow();
    if (!told) {
      told = true;
      deps.notify(reloadNow);
    }
  };
  const changed = () => {
    asked = true;
    tick();
  };
  return { changed, tick };
}

/** From main.tsx, once the app runs. */
export function startLanguageSync() {
  quietMs(); // the page's quiet counts from here
  const follower = languageFollower({
    differs: () => preferredLanguage() !== currentLanguage(),
    calm: pageCalm,
    reload: () => location.reload(),
    notify: (reloadNow) => {
      // Its words are in the catalog's rest, which may still be on its way this early: main.tsx's own imports, this one
      // included, do not wait for it (docs/web/web.md "Internationalization"). Without it, no toast: the page still
      // reloads at its next calm moment.
      const say = () => toast(t("language.other_tab"), "info", { label: t("language.other_tab_reload"), run: reloadNow, sticky: true });
      const rest = i18nRest();
      if (rest) rest.then(say, () => {});
      else say();
    },
  });
  addEventListener("storage", (e) => {
    if (e.key === null || e.key === "persona.language") follower.changed();
  });
  document.addEventListener("visibilitychange", follower.tick);
  setInterval(follower.tick, WAIT_TICK_MS);
  // a choice another tab made after this page read its language and before it listened
  if (preferredLanguage() !== currentLanguage()) follower.changed();
}
