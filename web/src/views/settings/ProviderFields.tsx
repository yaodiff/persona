// The provider wizard's form pieces (#2455): a labelled input, a secret one (keys, headers: never offered to the
// browser's password manager, shown only on request), the "Advanced" disclosure, a radio group of choices, and a check's
// status line.
import { useId, useState } from "react";
import { Collapse, cx, SECRET_INPUT, Spinner, Switch } from "../../ui/atoms";
import { CheckMark } from "../../ui/CheckMark";
import { Icon } from "../../ui/icons";
import { inputClass, textareaClass } from "../assistant/parts";
import { t } from "../../i18n";
import { referenceState, type SecretReference } from "../../lib/secret-reference";

export const INPUT = inputClass;
/** A field the form can't go on with (aria-invalid) has a red edge in place of its inset, as other settings forms draw
 *  one (the inset would win over an added edge); focused, it has the focus edge. */
const ringed = (base: string, error?: string) => (error ? base.replace("shadow-inset-field", "shadow-[inset_0_0_0_1px_var(--red)]") : base);
/** How long typed text has to stay the same before it is checked (a gateway's address and key) or read (a pasted
 *  configuration) on its own. */
export const AUTO_TEST_MS = 700;
/** A field's look without its size, for the inline ones (SMALL: in a model row). */
export const FIELD = "rounded-control bg-field text-ink shadow-inset-field outline-none placeholder:text-ink-3 focus:shadow-[inset_0_0_0_1px_var(--accent)] disabled:opacity-60";
export const SMALL = `${FIELD} h-7 px-2 text-[12px]`;

/** `describedById` goes on the hint / error line so the field can point `aria-describedby` at it. */
/** `accessory`: a control that belongs to this field (a switch), at the end of its label's row. */
/** `field`: which form field it is (`data-provider-field`), so the wizard can take the focus to the one with a problem. */
export function Labelled({ label, hint, error, children, htmlFor, describedById, accessory, field }: { label: React.ReactNode; hint?: React.ReactNode; error?: string; children: React.ReactNode; htmlFor?: string; describedById?: string; accessory?: React.ReactNode; field?: string }) {
  const caption = (
    <label htmlFor={htmlFor} className={cx("block text-[12px] font-medium text-ink-2", accessory ? "min-w-0 truncate" : "mb-1")}>
      {label}
    </label>
  );
  return (
    <div className="block min-w-0" data-provider-field={field}>
      {accessory ? <div className="mb-1 flex items-center justify-between gap-3">{caption}{accessory}</div> : caption}
      {children}
      {(error || hint) && <span id={describedById} className={cx("mt-1 block text-[11px] leading-relaxed", error ? "text-red" : "text-ink-3")}>{error || hint}</span>}
    </div>
  );
}

export function TextField({ label, value, onChange, hint, error, placeholder, mono, disabled, inputMode, onPaste, onBlur, field }: {
  label: React.ReactNode;
  value: string;
  onChange: (v: string) => void;
  hint?: React.ReactNode;
  error?: string;
  placeholder?: string;
  mono?: boolean;
  disabled?: boolean;
  inputMode?: "numeric" | "url";
  onPaste?: React.ClipboardEventHandler<HTMLInputElement>;
  onBlur?: React.FocusEventHandler<HTMLInputElement>;
  field?: string;
}) {
  const id = useId();
  const noteId = useId();
  return (
    <Labelled label={label} hint={hint} error={error} htmlFor={id} describedById={noteId} field={field}>
      <input id={id} aria-invalid={error ? true : undefined} aria-describedby={error || hint ? noteId : undefined} value={value}
        onChange={(e) => onChange(e.target.value)} onPaste={onPaste} onBlur={onBlur} disabled={disabled} placeholder={placeholder} inputMode={inputMode} autoComplete="off" spellCheck={false} className={cx(ringed(INPUT, error), mono && "font-mono")} />
    </Labelled>
  );
}

/** A key or other secret: masked, with "Show" / "Hide"; `multiline` for request headers (masked as discs until shown). */
export function SecretField({ label, value, onChange, hint, error, placeholder, multiline, accessory, field }: {
  label: React.ReactNode;
  value: string;
  onChange: (v: string) => void;
  hint?: React.ReactNode;
  error?: string;
  placeholder?: string;
  multiline?: boolean;
  accessory?: React.ReactNode;
  field?: string;
}) {
  const id = useId();
  const noteId = useId();
  const [shown, setShown] = useState(false);
  // password managers: none of them should offer to save a gateway key as the hub's password
  const quiet = { ...SECRET_INPUT, autoComplete: "off" } as const;
  const described = error || hint ? noteId : undefined;
  const invalid = error ? true : undefined;
  const toggle = (
    <button type="button" onClick={() => setShown((s) => !s)} aria-label={shown ? t("settings-providerfields.hide") : t("settings-providerfields.show")} aria-pressed={shown}
      className="touch-hit absolute top-1.5 right-1.5 flex size-6 items-center justify-center rounded-chip text-ink-3 hover:bg-hover hover:text-ink">
      <Icon name={shown ? "lock" : "key"} size={12} stroke={2} />
    </button>
  );
  return (
    <Labelled label={label} hint={hint} error={error} htmlFor={id} describedById={noteId} accessory={accessory} field={field}>
      <div className="relative">
        {multiline ? (
          <textarea id={id} aria-describedby={described} aria-invalid={invalid} value={value} onChange={(e) => onChange(e.target.value)} placeholder={placeholder} rows={3} {...quiet}
            className={cx(ringed(textareaClass, error), "pr-9 font-mono", !shown && "[-webkit-text-security:disc]")} />
        ) : (
          <input id={id} aria-describedby={described} aria-invalid={invalid} type={shown ? "text" : "password"} value={value} onChange={(e) => onChange(e.target.value)} placeholder={placeholder} {...quiet} autoComplete="new-password" className={cx(ringed(INPUT, error), "pr-9")} />
        )}
        {toggle}
      </div>
    </Labelled>
  );
}

/** A model provider's key, or a secret provider's reference in its place (docs/extensions/secret-providers.md "Launcher
 *  keys"): a "Use a reference" switch over the field where the hub takes references (`onByReference`), the reference in
 *  plain text, and what a stored reference names. As an extension's secret does (settings/Extensions.tsx). */
export function KeyField({ label, value, onChange, hint, error, placeholder, byReference, onByReference, stored, field }: {
  label: React.ReactNode;
  value: string;
  onChange: (v: string) => void;
  hint?: React.ReactNode;
  /** What keeps the form from going on, said under the field in place of the hint. */
  error?: string;
  field?: string;
  placeholder?: string;
  byReference: boolean;
  /** Absent: the hub takes no references, so there is no switch. */
  onByReference?: (on: boolean) => void;
  /** The stored key is this reference. */
  stored?: SecretReference | null;
}) {
  const id = useId();
  const noteId = useId();
  // on the field's own label row, so in a list of keys each switch sits with the key it changes
  const toggle = onByReference && (
    <span className="flex shrink-0 items-center gap-2 whitespace-nowrap text-[11.5px] text-ink-3">
      <span aria-hidden>{t("settings-providerfields.use_reference")}</span>
      <Switch label={t("settings-providerfields.use_reference")} on={byReference} onChange={onByReference} />
    </span>
  );
  if (!byReference) return <SecretField label={label} value={value} onChange={onChange} hint={hint} error={error} placeholder={placeholder} accessory={toggle} field={field} />;
  const note = stored ? `${referenceState(stored)}. ${t("settings-providerfields.reference_hint")}` : t("settings-providerfields.reference_hint");
  return (
    <Labelled label={label} hint={hint ? <span className="flex flex-col gap-0.5"><span>{note}</span>{hint}</span> : note} error={error} htmlFor={id} describedById={noteId} accessory={toggle} field={field}>
      <input id={id} aria-describedby={noteId} aria-invalid={error ? true : undefined} type="text" value={value} onChange={(e) => onChange(e.target.value)}
        placeholder={stored?.reference || t("settings-providerfields.reference_placeholder")} autoComplete="off" spellCheck={false}
        data-key-reference className={cx(ringed(INPUT, error), "font-mono")} />
    </Labelled>
  );
}

/** "Advanced": folded by default. */
export function Disclosure({ title, open, onToggle, children, badge }: { title: string; open: boolean; onToggle: () => void; children: React.ReactNode; badge?: string }) {
  return (
    <div className="rounded-card bg-inset shadow-hairline">
      <button type="button" onClick={onToggle} aria-expanded={open} className="touch-hit flex w-full items-center gap-2 px-3 py-2.5 text-left text-[12.5px] font-medium text-ink-2 hover:text-ink">
        <Icon name="chevron" size={10} stroke={2.4} className="transition-transform duration-150" style={{ transform: open ? "none" : "rotate(-90deg)" }} />
        <span className="whitespace-nowrap">{title}</span>
        {badge && <span className="truncate text-[11px] font-normal text-ink-3">{badge}</span>}
      </button>
      <Collapse open={open} lazy>
        <div className="flex flex-col gap-3 px-3 pt-1 pb-3">{children}</div>
      </Collapse>
    </div>
  );
}

// The tick of a role="checkbox" row (ui/CheckMark.tsx, which the chat draws too); Inbox imports it from here.
export { CheckMark };

/** Arrow keys move a radio group's choice to the next or previous of `count` (around the ends; from none, the first or
 *  the last), and focus follows (`attr` holds each option's index). */
export function arrowKeys(count: number, at: number, attr: string, pick: (i: number) => void) {
  return (e: React.KeyboardEvent<HTMLDivElement>) => {
    const step = e.key === "ArrowDown" || e.key === "ArrowRight" ? 1 : e.key === "ArrowUp" || e.key === "ArrowLeft" ? -1 : 0;
    if (!step) return;
    e.preventDefault();
    const i = at < 0 ? (step > 0 ? 0 : count - 1) : (at + step + count) % count;
    pick(i);
    e.currentTarget.querySelector<HTMLElement>(`[${attr}="${i}"]`)?.focus();
  };
}

/** A radio group of rows, each a label with a hint under it; `at` is the chosen one (-1: none, the first takes the tab). */
export function ChoiceRows({ label, options, at, onPick }: { label: string; options: { label: string; hint: string }[]; at: number; onPick: (i: number) => void }) {
  return (
    <div role="radiogroup" aria-label={label} onKeyDown={arrowKeys(options.length, at, "data-choice", onPick)}
      className="divide-y divide-line overflow-hidden rounded-card bg-inset shadow-hairline">
      {options.map((o, i) => {
        const checked = i === at;
        return (
          <button key={i} type="button" role="radio" aria-checked={checked} tabIndex={checked || (at < 0 && i === 0) ? 0 : -1} data-choice={i}
            onClick={() => onPick(i)} className="touch-hit flex w-full items-start gap-2.5 px-3 py-2 text-left transition-colors hover:bg-hover">
            <CheckMark on={checked} round className="mt-0.5" />
            <span className="min-w-0 flex-1">
              <span className="block truncate text-[12.5px] font-medium text-ink">{o.label}</span>
              <span className="block text-[11.5px] text-ink-3">{o.hint}</span>
            </span>
          </button>
        );
      })}
    </div>
  );
}

/** A check running: a spinner and what it is doing. */
export function BusyLine({ children }: { children: React.ReactNode }) {
  return (
    <p role="status" className="flex items-center gap-1.5 text-[12px] text-ink-2">
      <Spinner />
      {children}
    </p>
  );
}

/** The one action at the end of a check's failed line (Retry, Test again, Use another), in the line's color. */
export function LineAction({ onClick, children }: { onClick: () => void; children: React.ReactNode }) {
  return (
    <button type="button" onClick={onClick} className="touch-hit shrink-0 whitespace-nowrap font-medium hover:underline">
      {children}
    </button>
  );
}
