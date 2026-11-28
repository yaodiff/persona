// The key bar's line box (#14527): one line typed into the terminal and run with Return, hidden while it is typed, for a
// prompt in the pane itself (a password, a pasted token, a sign-in code) where the terminal would show what a phone's
// keyboard suggests and keeps. The line lives only in this box's state and its input, never in drafts or storage, and is
// emptied as it goes in and when the box closes. Enter in the field sends it, as Return would in the terminal.
import { useEffect, useLayoutEffect, useRef, useState } from "react";
import { t } from "../../../i18n";
import { EASE, IconButton, SECRET_INPUT } from "../../../ui/atoms";

export function LineBox({ onSend, onClose }: { onSend: (line: string) => void; onClose: () => void }) {
  const [line, setLine] = useState("");
  const [shown, setShown] = useState(false);
  const ref = useRef<HTMLDivElement>(null);
  const input = useRef<HTMLInputElement>(null);
  useEffect(() => {
    input.current?.focus();
    const down = (e: PointerEvent) => {
      if (ref.current?.contains(e.target as Node)) return;
      if ((e.target as Element | null)?.closest?.(`[aria-label="${t("session-terminal-keybar.line")}"]`)) return; // its own key toggles it
      onClose();
    };
    const key = (e: KeyboardEvent) => e.key === "Escape" && onClose();
    addEventListener("pointerdown", down, true);
    addEventListener("keydown", key);
    return () => {
      removeEventListener("pointerdown", down, true);
      removeEventListener("keydown", key);
    };
  }, [onClose]);
  // a field taken off the page keeps its value until collected: emptied before React removes it
  useLayoutEffect(() => {
    const el = input.current;
    return () => {
      if (el) el.value = "";
    };
  }, []);
  const send = () => {
    if (line === "") return;
    onSend(line);
    setLine("");
    onClose();
  };
  return (
    <div
      ref={ref}
      role="dialog"
      aria-label={t("session-terminal-keybar.line_title")}
      className="absolute bottom-full left-1.5 z-20 mb-1.5 w-[min(340px,calc(100%-12px))] rounded-card bg-surface p-3 shadow-overlay"
      style={{ animation: `pop-in 160ms ${EASE} both`, transformOrigin: "bottom left" }}
    >
      <div className="mb-2 text-submeta font-medium text-ink-3">{t("session-terminal-keybar.line_title")}</div>
      <div className="flex items-center gap-1.5">
        {/* The line a program in the terminal asks for, not this site's password: "one-time-code" keeps browsers from
            offering to save it; no name, no form. */}
        <input
          ref={input}
          type={shown ? "text" : "password"}
          value={line}
          onChange={(e) => setLine(e.target.value)}
          onKeyDown={(e) => {
            if (e.key !== "Enter" || e.nativeEvent.isComposing) return;
            e.preventDefault();
            send();
          }}
          aria-label={t("session-terminal-keybar.line_title")}
          placeholder={t("session-terminal-keybar.line_placeholder")}
          {...SECRET_INPUT}
          autoComplete="one-time-code"
          autoCapitalize="off"
          autoCorrect="off"
          enterKeyHint="send"
          className="h-8 w-full min-w-0 rounded-control bg-inset px-2.5 font-mono text-meta text-ink outline-none placeholder:font-sans placeholder:text-ink-3"
        />
        <IconButton icon={shown ? "eyeOff" : "eye"} label={shown ? t("session-terminal-keybar.line_hide") : t("session-terminal-keybar.line_show")} pressed={shown}
          onClick={() => setShown((v) => !v)} />
        <IconButton icon="enter" label={t("session-terminal-keybar.line_send")} disabled={line === ""} onClick={send} />
      </div>
      <p className="mt-2 text-label leading-snug text-ink-3">{t("session-terminal-keybar.line_hint")}</p>
    </div>
  );
}
