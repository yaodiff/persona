// A configuration form (lib/configuration, docs/web/web.md "Configuration forms"; docs/design/DESIGN_SYSTEM.md
// "Configuration forms"): one Document from an injected source, drawn with the settings pieces every page uses. It knows
// no module: titles, descriptions, details and notices are the source's words, shown as given.
//
// Every field ends in one footer row: its default value, the count where a text field has a limit, the save state, and
// the Reset / Cancel / Save actions that apply. The row is the same for every kind, so a row stays short and the actions
// never sit between the control and its explanation.
//
// A single-step choice (switch, selection) is written at once and shows as pending until the source has committed it;
// typed text and numbers are drafts until Save (Enter on one line), and a multiselect's checks are a pending selection
// with Save and Cancel. A multiselect whose maximum is one is drawn and operated as a single choice: choosing another
// option replaces the old one locally (the wire value stays an array), so a replacement never shows two chosen or a
// too-many error. Drafts stay through any failure or conflict. One write at a time, and a conflict or an uncertain write
// needs Reload first.
import { useEffect, useId, useLayoutEffect, useRef, useState, useSyncExternalStore, type ReactNode } from "react";
import { formatList, t } from "../../../i18n";
import { ConfigurationController, draftValue, type FieldState, type FormState, type Problem } from "../../../lib/configuration/controller";
import { ERROR_CODES, fieldValue, sameValue, scalarLength, type ConfigurationField, type ConfigurationSource, type FieldValue, type KnownField } from "../../../lib/configuration/contract";
import { invalidConditions, type ConfigurationGuidance, type GuidanceNote } from "../../../lib/configuration/guidance";
import { Button, Dialog, Loading, Spinner } from "../../../ui/atoms";
import { CheckMark } from "../../../ui/CheckMark";
import { inputClass, Segmented, textareaClass } from "../../assistant/parts";
import { ChoiceRows } from "../ProviderFields";
import { Group, RowFailed, settingsInputClass, Switch } from "../SettingsParts";

/** What went wrong, in this client's words for a code it knows, else the source's own (localized) message, else a
 *  generic line. */
export function problemText(p: Problem): string {
  if (p.code === "invalid_guidance") return t("settings-configuration.invalid_guidance");
  if ((ERROR_CODES as readonly string[]).includes(p.code)) return t(`settings-configuration.error_${p.code}`);
  return p.message || t("settings-configuration.error_failed");
}

/** The form for `source`. A source with another `key` is another resource: the form starts over for it. */
type FieldBadge = { id: string; content: ReactNode };

export function ConfigurationForm({ source, guidance, fieldBadge, afterFields }: { source: ConfigurationSource; guidance?: ConfigurationGuidance; fieldBadge?: FieldBadge; afterFields?: ReactNode }) {
  const [ctl, setCtl] = useState<ConfigurationController | null>(null);
  useEffect(() => {
    const c = new ConfigurationController(source, guidance);
    setCtl(c);
    c.start();
    return () => c.dispose();
    // one controller per resource: the same key is the same resource
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [source.key]);
  useLayoutEffect(() => ctl?.setGuidance(guidance), [ctl, guidance]);
  if (!ctl || ctl.source.key !== source.key) return <FormLoading />;
  return <ConfigurationFormView ctl={ctl} fieldBadge={fieldBadge} afterFields={afterFields} />;
}

function FormLoading() {
  return (
    <Group>
      <div className="px-4 py-3">
        <Loading />
      </div>
    </Group>
  );
}

export function ConfigurationFormView({ ctl, fieldBadge, afterFields, onReload = () => ctl.reload() }: {
  ctl: ConfigurationController;
  fieldBadge?: FieldBadge;
  afterFields?: ReactNode;
  onReload?: () => void;
}) {
  const s = useSyncExternalStore(ctl.subscribe, ctl.snapshot);
  const heading = useRef<HTMLHeadingElement>(null);
  const previouslyConfirming = useRef(false);
  const request = s.confirmation;
  const descriptionId = useId();
  useEffect(() => {
    if (!request && previouslyConfirming.current && document.activeElement === document.body) {
      heading.current?.focus({ preventScroll: true });
    }
    previouslyConfirming.current = !!request;
  }, [request]);
  if (s.phase === "loading" || !s.doc) {
    if (s.phase !== "failed") return <FormLoading />;
    return (
      <Group>
        <RowFailed text={problemText(s.readError ?? { code: "failed" })} retry={t("settings-configuration.retry")} onRetry={onReload} />
      </Group>
    );
  }
  const doc = s.doc;
  return (
    <section aria-label={doc.title} data-configuration={doc.id}>
      <div className="mb-1.5 px-1">
        <h2 ref={heading} tabIndex={-1} className="text-label font-medium text-ink-3">{doc.title}</h2>
        {doc.description && <p className="mt-0.5 text-submeta leading-relaxed text-ink-3">{doc.description}</p>}
        <Notes notes={ctl.guidance?.notes} />
      </div>
      {!s.writable && <Banner tone="caution">{t("settings-configuration.unsupported_version")}</Banner>}
      {s.blocked && (
        <Banner tone="alert" action={<Button size="sm" icon="retry" onClick={onReload}>{t("settings-configuration.reload")}</Button>}>
          {problemText(s.blocked)}
        </Banner>
      )}
      {s.readError && (
        <Banner tone="alert" action={<Button size="sm" icon="retry" onClick={onReload}>{t("settings-configuration.retry")}</Button>}>
          {t("settings-configuration.refresh_failed", { error: problemText(s.readError) })}
        </Banner>
      )}
      {s.formError && <Banner tone="alert">{problemText(s.formError)}</Banner>}
      {doc.groups.map((g, i) => (
        <Group key={g.id} title={g.title || undefined} footer={g.description} anchor={`configuration-${doc.id}-${g.id}`}>
          {g.fields.map((f) => (
            <FieldRow key={f.id} ctl={ctl} s={s} field={f} badge={fieldBadge?.id === f.id ? fieldBadge.content : undefined} />
          ))}
          {i === doc.groups.length - 1 ? afterFields : null}
        </Group>
      ))}
      {request && (
        <Dialog open title={request.copy.title} describedBy={descriptionId} onClose={() => ctl.cancel(request)}
          footer={<div className="flex min-w-0 max-w-full flex-wrap justify-end gap-2">
            <Button onClick={() => ctl.cancel(request)}>{t("settings-configuration.cancel")}</Button>
            <Button tone="primary" className="max-w-full" onClick={() => ctl.confirm(request)}>
              <span className="truncate">{request.copy.confirmLabel}</span>
            </Button>
          </div>}>
          <p id={descriptionId} className="text-meta leading-relaxed text-ink">{request.copy.body}</p>
          <dl className="mt-2 grid grid-cols-[auto_1fr] gap-x-3 gap-y-1 text-submeta leading-relaxed">
            <Detail term={t("settings-labs.scope")}>{request.copy.scope}</Detail>
            <Detail term={t("settings-labs.impact")}>{request.copy.impact}</Detail>
            <Detail term={t("settings-labs.activation")}>{request.copy.activation}</Detail>
          </dl>
        </Dialog>
      )}
    </section>
  );
}

/** A line across the form: a version it can't write, a conflict, a failed refresh. */
function Banner({ tone, action, children }: { tone: "caution" | "alert"; action?: React.ReactNode; children: React.ReactNode }) {
  return (
    <div className="mb-3 flex flex-wrap items-center gap-x-3 gap-y-2 rounded-card bg-surface px-4 py-2.5 shadow-card">
      <p role={tone === "alert" ? "alert" : undefined} className={tone === "alert" ? "min-w-0 flex-1 text-meta leading-snug text-red" : "min-w-0 flex-1 text-meta leading-snug text-orange"}>{children}</p>
      {action}
    </div>
  );
}

/** A value as a line of text (the default value in the footer). */
function valueText(field: KnownField, v: FieldValue): string {
  switch (field.kind) {
    case "toggle":
      return v ? t("settings-configuration.on") : t("settings-configuration.off");
    case "select":
      return field.select.options.find((o) => o.value === v)?.title ?? String(v);
    case "multiselect": {
      const titles = field.multiselect.options.filter((o) => (v as string[]).includes(o.value)).map((o) => o.title);
      return titles.length ? formatList(titles) : t("settings-configuration.none");
    }
    case "text":
      return (v as string) || t("settings-configuration.empty");
    case "number":
      return String(v);
  }
}

function FieldRow({ ctl, s, field, badge }: { ctl: ConfigurationController; s: FormState; field: ConfigurationField; badge?: ReactNode }) {
  const titleId = useId();
  const descId = useId();
  const countId = useId();
  const invalidId = useId();
  const doc = s.doc!;
  const fs: FieldState = s.fields[field.id] ?? {};
  const notice = doc.notices?.[field.id];
  const known = field.kind === "unsupported" ? null : field;
  const value = known ? fieldValue(doc, known) : undefined;
  const can = known ? ctl.canWrite(field.id) : false;
  // typing and checking stay possible while another field is being written (a draft is local); only Save waits
  const guidance = ctl.guidance?.fields?.[field.id];
  const editable = ctl.editable(field.id);
  // a multiselect's unsaved checks are what it shows; its pending write once committed
  const checked = known?.kind === "multiselect" && fs.selection !== undefined ? (fs.selection as FieldValue) : undefined;
  const shown = fs.pending ?? checked ?? value;
  const inline = known && (known.kind === "toggle" || (known.kind === "select" && (known.select.presentation === "segmented" || known.select.presentation === "dropdown")));
  const selectionDirty = !!known && known.kind === "multiselect" && fs.selection !== undefined && !sameValue(fs.selection, value);
  const typed = !!known && (known.kind === "text" || known.kind === "number");
  const committed = typed && shown !== undefined ? String(shown) : "";
  const text = fs.draft ?? committed;
  const typedDirty = typed && fs.draft !== undefined && (shown === undefined || fs.draft !== committed);
  const count = known?.kind === "text" && known.text.max_length !== undefined ? t("settings-configuration.count", { n: scalarLength(text), max: known.text.max_length }) : undefined;
  const invalid = !!known && value === undefined;
  const described = [field.description ? descId : undefined, count !== undefined ? countId : undefined, invalid ? invalidId : undefined].filter(Boolean).join(" ") || undefined;
  const resettable = !!known && editable && !selectionDirty && (value === undefined || !sameValue(value, known.default));

  return (
    <div data-settings-row={`configuration-${doc.id}-${field.id}`} data-configuration-field={field.id} data-kind={field.kind} className="px-4 py-2.5">
      <div className="flex flex-col gap-x-3 gap-y-1.5 sm:flex-row sm:items-start">
        <div className="min-w-0 flex-1">
          {badge ? (
            <div className="flex flex-wrap items-center gap-x-2 gap-y-1 text-meta text-ink">
              <span id={titleId}>{field.title}</span>
              {badge}
            </div>
          ) : <div id={titleId} className="text-meta text-ink">{field.title}</div>}
          {field.description && <p id={descId} className="mt-0.5 text-submeta leading-snug text-ink-3">{field.description}</p>}
        </div>
        {known && inline && <div className="shrink-0"><InlineControl ctl={ctl} field={known} shown={shown} can={can} titleId={titleId} descId={described} invalidValue={invalid} /></div>}
      </div>
      {known && !inline && (
        <div className="mt-2">
          <BlockControl ctl={ctl} field={known} shown={shown} fs={fs} can={can} editable={editable} titleId={titleId} descId={described} dirty={typedDirty} />
        </div>
      )}
      {fs.error && <p role="alert" className="mt-1.5 text-submeta leading-snug text-red">{problemText(fs.error)}</p>}
      <Notes notes={guidance?.notes} />
      {known && invalidConditions(known, guidance?.rules) && <Note tone="caution">{t("settings-configuration.invalid_guidance")}</Note>}
      {notice && <Note tone={notice.kind === "warning" ? "caution" : undefined}>{notice.message}</Note>}
      {field.kind === "unsupported" && <Note tone="caution">{field.reason === "kind" ? t("settings-configuration.unsupported_kind") : t("settings-configuration.unsupported_definition")}</Note>}
      {invalid && <Note id={invalidId} tone="caution">{t("settings-configuration.invalid_value")}</Note>}
      {known?.disabled_reason ? <Note>{known.disabled_reason}</Note> : known?.read_only ? <Note>{t("settings-configuration.read_only")}</Note> : null}
      {known && known.details?.length ? (
        <dl className="mt-1.5 grid grid-cols-[auto_1fr] gap-x-3 gap-y-0.5 text-submeta leading-snug">
          {known.details.map((d, i) => <Detail key={i} term={d.title}>{d.description}</Detail>)}
        </dl>
      ) : null}
      {!known && field.details?.length ? (
        <dl className="mt-1.5 grid grid-cols-[auto_1fr] gap-x-3 gap-y-0.5 text-submeta leading-snug">
          {field.details.map((d, i) => <Detail key={i} term={d.title}>{d.description}</Detail>)}
        </dl>
      ) : null}
      {known && <FieldFooter ctl={ctl} field={known} can={can} resettable={resettable} dirty={typedDirty || selectionDirty} count={count} countId={countId} busy={s.busy === field.id} saved={fs.saved} />}
    </div>
  );
}

/** One footer row for a field: its default and count, the save state, and Reset / Cancel / Save. */
function FieldFooter({ ctl, field, can, resettable, dirty, count, countId, busy, saved }: {
  ctl: ConfigurationController;
  field: KnownField;
  can: boolean;
  resettable: boolean;
  dirty: boolean;
  count?: string;
  countId: string;
  busy: boolean;
  saved?: FieldState["saved"];
}) {
  return (
    <div className="mt-2 flex flex-wrap items-center gap-x-2 gap-y-1">
      <span className="min-w-0 text-submeta leading-snug text-ink-3">
        {t("settings-configuration.default")} <span className="text-ink-2">{valueText(field, field.default)}</span>
      </span>
      {count !== undefined && <span id={countId} className="text-submeta text-ink-3 tabular-nums">{count}</span>}
      {/* Reserve the small buttons' height even when there is only text on this line. */}
      <span className={typeof saved === "string" ? "ml-auto flex min-h-7 max-w-full flex-wrap items-center justify-end gap-1.5" : "ml-auto flex min-h-7 shrink-0 items-center gap-1.5"}>
        <span aria-live="polite" className={busy || saved ? "flex items-center gap-1.5 text-submeta leading-snug" : "sr-only"}>
          {busy && <><Spinner />{t("settings-configuration.saving")}</>}
          {saved && <span className="text-green">{saved === true ? t("settings-configuration.saved") : saved}</span>}
        </span>
        {resettable && (
          <Button size="sm" tone="quiet" icon="undo" disabled={!can} onClick={() => ctl.reset(field.id)}>{t("settings-configuration.reset")}</Button>
        )}
        {dirty && (
          <>
            <Button size="sm" tone="quiet" onClick={() => ctl.discard(field.id)}>{t("settings-configuration.cancel")}</Button>
            <Button size="sm" tone="primary" disabled={!can} onClick={() => ctl.saveDraft(field.id)}>{t("settings-configuration.save")}</Button>
          </>
        )}
      </span>
    </div>
  );
}

function Detail({ term, children }: { term: string; children: React.ReactNode }) {
  return (
    <>
      <dt className="text-ink-3">{term}</dt>
      <dd className="min-w-0 text-ink-2">{children}</dd>
    </>
  );
}

function Notes({ notes }: { notes?: readonly GuidanceNote[] }) {
  return notes?.map((note, i) => note?.text?.trim() && (
    <Note key={i} tone={note.kind === "warning" ? "caution" : undefined}>{note.text}</Note>
  ));
}

function Note({ tone, id, children }: { tone?: "caution"; id?: string; children: React.ReactNode }) {
  return <p id={id} className={tone ? "mt-1.5 text-submeta leading-snug text-orange" : "mt-1.5 text-submeta leading-snug text-ink-3"}>{children}</p>;
}

interface ControlProps {
  ctl: ConfigurationController;
  field: KnownField;
  shown: FieldValue | undefined;
  titleId: string;
  descId?: string;
}

/** Controls at the end of the title row: a switch, a segmented choice, a dropdown. */
function InlineControl({ ctl, field, shown, can, titleId, descId, invalidValue }: ControlProps & { can: boolean; invalidValue: boolean }) {
  if (field.kind === "toggle") {
    // With no saved boolean, offer both values without implying either was already set.
    if (invalidValue) return (
      <fieldset disabled={!can} aria-describedby={descId}>
        <Segmented<string> label={field.title} value={shown === undefined ? "" : shown ? "on" : "off"}
          options={[{ v: "off", label: t("settings-configuration.off") }, { v: "on", label: t("settings-configuration.on") }]}
          onChange={(v) => ctl.choose(field.id, v === "on")} />
      </fieldset>
    );
    return <Switch on={shown === true} onChange={(on) => ctl.choose(field.id, on)} disabled={!can} label={field.title} describedBy={descId} />;
  }
  if (field.kind !== "select") return null;
  const selected = shown === undefined ? "" : (shown as string);
  if (field.select.presentation === "dropdown") {
    return (
      <select aria-labelledby={titleId} aria-describedby={descId} className={settingsInputClass} value={selected} disabled={!can} onChange={(e) => ctl.choose(field.id, e.target.value)}>
        {shown === undefined && <option value="" disabled hidden>{t("settings-configuration.none")}</option>}
        {field.select.options.map((o) => <option key={o.value} value={o.value}>{o.title}</option>)}
      </select>
    );
  }
  return (
    <fieldset disabled={!can} aria-describedby={descId}>
      <Segmented<string> label={field.title} value={selected} options={field.select.options.map((o) => ({ v: o.value, label: o.title }))} onChange={(v) => ctl.choose(field.id, v)} />
    </fieldset>
  );
}

/** Controls under the title: a list of choices, a list of checks, a text or number field. */
function BlockControl({ ctl, field, shown, fs, can, editable, titleId, descId, dirty }: ControlProps & { fs: FieldState; can: boolean; editable: boolean; dirty: boolean }) {
  if (field.kind === "select") {
    const options = field.select.options;
    return (
      <fieldset disabled={!can} aria-describedby={descId}>
        <ChoiceRows label={field.title} options={options.map((o) => ({ label: o.title, hint: o.description ?? "" }))} at={options.findIndex((o) => o.value === shown)} onPick={(i) => ctl.choose(field.id, options[i].value)} />
      </fieldset>
    );
  }
  if (field.kind === "multiselect") return <Checks ctl={ctl} field={field} chosen={(shown as string[] | undefined) ?? []} editable={editable} titleId={titleId} descId={descId} />;
  if (field.kind === "text" || field.kind === "number") return <Typed ctl={ctl} field={field} shown={shown} fs={fs} editable={editable} titleId={titleId} descId={descId} dirty={dirty} />;
  return null;
}

function Checks({ ctl, field, chosen, editable, titleId, descId }: { ctl: ConfigurationController; field: Extract<KnownField, { kind: "multiselect" }>; chosen: string[]; editable: boolean; titleId: string; descId?: string }) {
  const options = field.multiselect.options;
  // A maximum of one is a single choice: clicking another option replaces the old one at once (the value stays an array on
  // the wire). Any other maximum keeps ordinary checks, where a draft can pass through a temporary count.
  const single = field.multiselect.max_selected === 1;
  const canClear = single && (field.multiselect.min_selected ?? 0) === 0;
  const pick = (v: string) => {
    if (single) ctl.setSelection(field.id, canClear && chosen.includes(v) ? [] : [v]);
    else {
      const on = chosen.includes(v);
      // in the options' order, whatever order they were picked in
      ctl.setSelection(field.id, options.map((o) => o.value).filter((x) => (x === v ? !on : chosen.includes(x))));
    }
  };
  const onKeyDown = single
    ? (e: React.KeyboardEvent<HTMLDivElement>) => {
        if (!editable) return;
        const keys = e.key === "ArrowDown" || e.key === "ArrowRight" ? 1 : e.key === "ArrowUp" || e.key === "ArrowLeft" ? -1 : 0;
        if (!keys) return;
        e.preventDefault();
        const at = options.findIndex((o) => chosen.includes(o.value));
        const next = (at < 0 ? 0 : at + keys + options.length) % options.length;
        ctl.setSelection(field.id, [options[next].value]);
        e.currentTarget.querySelector<HTMLElement>(`[data-choice="${next}"]`)?.focus();
      }
    : undefined;
  return (
    <div role={single ? "radiogroup" : "group"} aria-labelledby={titleId} aria-describedby={descId} onKeyDown={onKeyDown}
      className="divide-y divide-line overflow-hidden rounded-card bg-inset shadow-hairline">
      {options.map((o, i) => {
        const on = chosen.includes(o.value);
        return (
          <button key={o.value} type="button" role={single ? "radio" : "checkbox"} aria-checked={on} disabled={!editable}
            tabIndex={single ? (on || (!chosen.length && i === 0) ? 0 : -1) : undefined} data-choice={single ? i : undefined}
            onClick={() => pick(o.value)}
            className="touch-hit flex w-full items-start gap-2.5 px-3 py-2 text-left transition-colors hover:bg-hover disabled:opacity-60">
            <CheckMark on={on} round={single} className="mt-0.5" />
            <span className="min-w-0 flex-1">
              <span className="block truncate text-meta font-medium text-ink">{o.title}</span>
              {o.description && <span className="block text-submeta text-ink-3">{o.description}</span>}
            </span>
          </button>
        );
      })}
    </div>
  );
}

function Typed({ ctl, field, shown, fs, editable, titleId, descId, dirty }: ControlProps & { fs: FieldState; editable: boolean; dirty: boolean }) {
  const committed = shown === undefined ? "" : String(shown);
  const text = fs.draft ?? committed;
  const multiline = field.kind === "text" && field.text?.multiline;
  const onKey = (e: React.KeyboardEvent<HTMLInputElement | HTMLTextAreaElement>) => {
    if (e.nativeEvent.isComposing) return;
    if (e.key === "Escape" && dirty) {
      e.preventDefault();
      e.stopPropagation();
      ctl.discard(field.id);
    } else if (e.key === "Enter" && !multiline) {
      e.preventDefault();
      if (dirty) ctl.saveDraft(field.id);
    } else if (field.kind === "number" && (e.key === "ArrowUp" || e.key === "ArrowDown")) {
      // the step is a nudge, not a rule: the result is checked like anything typed
      const r = draftValue(field, text);
      const base = "value" in r ? (r.value as number) : field.default;
      const step = field.number?.step ?? 1;
      e.preventDefault();
      ctl.setDraft(field.id, String(base + (e.key === "ArrowUp" ? step : -step)));
    }
  };
  const common = {
    "aria-labelledby": titleId,
    "aria-describedby": descId,
    "aria-invalid": fs.error || shown === undefined ? true : undefined,
    value: text,
    disabled: !editable,
    onKeyDown: onKey,
    placeholder: field.kind === "text" ? field.text?.placeholder : undefined,
    spellCheck: false,
    autoComplete: "off",
  };
  if (multiline) return <textarea {...common} rows={3} className={textareaClass} onChange={(e) => ctl.setDraft(field.id, e.target.value)} />;
  return (
    <input {...common} inputMode={field.kind === "number" ? (field.number?.integer ? "numeric" : "decimal") : undefined}
      className={field.kind === "number" ? `${inputClass} max-w-[9rem]` : inputClass} onChange={(e) => ctl.setDraft(field.id, e.target.value)} />
  );
}
