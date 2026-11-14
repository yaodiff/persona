// "Overview": the hub at a glance — its name, build, uptime, how it listens, the addresses it hands out, counts, doctor. With
// no computer but its own yet, adding one comes first (docs/archive/operations/plan-hub-center.md 0).
import { useState } from "react";
import { t } from "../../i18n";
import { Button, cx, IconButton, toast, useNow } from "../../ui/atoms";
import { adminApi, errorText } from "../api";
import { AdminIcon } from "../icons";
import { dateTime, endpointKindLabel, groupDoctor, needsMachines, uptime } from "../logic";
import { navigate } from "../router";
import { share } from "../store";
import type { DoctorReport, Endpoint, MachinesView, Overview } from "../types";
import { Card, CopyValue, Dot, ErrorBox, Line, Loader, PageHeader, Stat, Tag, useLoad } from "../parts";

export function OverviewPage() {
  const { data, error, loading, reload, set } = useLoad(async () => {
    const o = await adminApi<Overview>("/overview");
    share({ restart: o.restart, canRestart: o.hub.can_restart, tls: o.tls, hubName: o.hub.name, startedAt: o.hub.started_at, hubId: o.hub.hub_id });
    return o;
  });
  // The counts cannot tell the hub's own machine from a computer: the list can.
  const list = useLoad(() => adminApi<MachinesView>("/machines"));
  const now = useNow(30_000);
  if (!data) return loading ? <Loader /> : <ErrorBox error={error} onRetry={reload} />;
  const o = data;
  const empty = list.data ? needsMachines(list.data.machines) : list.error ? o.machines.total === 0 : false;
  return (
    <>
      <PageHeader title={t("admin-pages-overview.title")} subtitle={t("admin-pages-overview.subtitle")} />
      {empty && <AddFirst />}
      <div className="mb-6 grid grid-cols-2 gap-3 sm:grid-cols-4">
        <Stat icon="pulse" label={t("admin-pages-overview.stat_uptime")} value={uptime(o.hub.started_at, now)} hint={t("admin-pages-overview.stat_uptime_since", { time: dateTime(o.hub.started_at) })} />
        <Stat icon="machine" label={t("admin-pages-overview.stat_computers_online")} value={`${o.machines.online}/${o.machines.total}`} hint={t("admin-pages-overview.stat_view_computers")} onClick={() => navigate("machines")} />
        <Stat icon="phone" label={t("admin-pages-overview.stat_devices")} value={o.devices} hint={t("admin-pages-overview.stat_devices_hint")} onClick={() => navigate("devices")} />
        <Stat icon="lock" label="TLS" value={o.tls ? t("admin-pages-overview.tls_on") : t("admin-pages-overview.tls_off")} hint={o.tls ? t("admin-pages-overview.tls_port", { port: o.tls.port || o.tls.listen }) : t("admin-pages-overview.tls_enable_hint")} onClick={() => navigate("network")} />
      </div>

      <Card title="hub">
        <NameLine o={o} onSaved={(name, nameSet) => set({ ...o, hub: { ...o.hub, name, name_set: nameSet } })} />
        <Line label={t("admin-pages-overview.version")}>
          <CopyValue value={o.hub.version} />
        </Line>
        <Line label={t("admin-pages-overview.system")}>
          <span className="font-mono text-[12px]">
            {o.hub.os}/{o.hub.arch}
          </span>
          {o.hub.container && <Tag>{t("admin-pages-overview.container")}</Tag>}
        </Line>
        <Line label={t("admin-pages-overview.hub_id")} hint={t("admin-pages-overview.hub_id_hint")}>
          <CopyValue value={o.hub.hub_id} />
        </Line>
      </Card>

      <Card title={t("admin-pages-overview.connection")} footer={t("admin-pages-overview.connection_footer")}>
        <Line label={t("admin-pages-overview.plain_listen")} hint={t("admin-pages-overview.plain_listen_hint")}>
          <span className="font-mono text-[12px]">{o.listen}</span>
        </Line>
        <Line label={t("admin-pages-overview.public_url")} hint={t("admin-pages-overview.public_url_hint")}>
          <CopyValue value={o.public_url || "—"} />
        </Line>
        {o.tls ? (
          <>
            <Line label={t("admin-pages-overview.tls_listen")} hint={t("admin-pages-overview.tls_listen_hint")}>
              <span className="font-mono text-[12px]">{o.tls.listen}</span>
              <Tag tone="green">{t("admin-pages-overview.tls_on")}</Tag>
            </Line>
            <Line label={t("admin-pages-overview.fingerprint")} hint={t("admin-pages-overview.fingerprint_hint")}>
              <CopyValue value={o.tls.fp} className="max-w-[340px]" />
            </Line>
          </>
        ) : (
          <Line label={t("admin-pages-overview.tls_listen")} hint={t("admin-pages-overview.tls_off_hint")}>
            <Tag>{t("admin-pages-overview.tls_off")}</Tag>
            <Button className="h-7 px-2.5" onClick={() => navigate("network")}>
              {t("admin-pages-overview.enable")}
            </Button>
          </Line>
        )}
      </Card>

      <Card title={t("admin-pages-overview.endpoints")} footer={t("admin-pages-overview.endpoints_footer")}>
        <Endpoints list={o.endpoints} />
      </Card>

      <Doctor />
    </>
  );
}

// AddFirst: nothing but the hub's own machine yet. One hub is enough: every other computer gets the node service and is
// added here (docs/archive/operations/plan-hub-center.md 0).
function AddFirst() {
  return (
    <section className="mb-6 flex flex-wrap items-center gap-x-4 gap-y-3 rounded-card bg-surface px-4 py-4 shadow-card" style={{ animation: "fade-in 200ms ease-out both" }}>
      <span className="flex size-10 shrink-0 items-center justify-center rounded-[12px] bg-accent-tint text-accent-ink">
        <AdminIcon name="machine" size={19} />
      </span>
      <div className="min-w-[220px] flex-1">
        <div className="text-[14px] font-semibold text-ink">{t("admin-pages-overview.add_first_title")}</div>
        <p className="mt-0.5 text-[12.5px] leading-relaxed text-ink-2">
          {t("admin-pages-overview.add_first_body")}
        </p>
      </div>
      <Button tone="primary" icon="plus" onClick={() => navigate("machines")}>
        {t("admin-pages-overview.add_computer")}
      </Button>
    </section>
  );
}

function NameLine({ o, onSaved }: { o: Overview; onSaved: (name: string, nameSet: boolean) => void }) {
  const [editing, setEditing] = useState(false);
  const [value, setValue] = useState(o.hub.name_set ? o.hub.name : "");
  const [busy, setBusy] = useState(false);
  const save = async (e?: React.FormEvent) => {
    e?.preventDefault();
    setBusy(true);
    try {
      const r = await adminApi<{ name: string; name_set: boolean }>("/hub", "PUT", { name: value.trim() });
      onSaved(r.name, r.name_set);
      share({ hubName: r.name });
      setEditing(false);
      toast(t("admin-pages-overview.saved"), "ok");
    } catch (err) {
      toast(errorText(err), "error");
    } finally {
      setBusy(false);
    }
  };
  if (editing)
    return (
      <form onSubmit={save} className="flex flex-wrap items-center gap-2 px-4 py-2.5">
        <span className="min-w-[120px] flex-1 text-[13px] text-ink">{t("admin-pages-overview.name")}</span>
        <input
          autoFocus
          value={value}
          onChange={(e) => setValue(e.target.value)}
          placeholder={o.hub.default_name}
          aria-label={t("admin-pages-overview.name_label")}
          maxLength={64}
          className="h-8 w-[220px] max-w-full rounded-control bg-inset px-2.5 text-[13px] text-ink shadow-hairline outline-none placeholder:text-ink-3 focus:shadow-[0_0_0_1px_var(--line-strong),0_0_0_3px_var(--accent-tint)]"
        />
        <Button tone="quiet" className="h-8" onClick={() => setEditing(false)}>
          {t("common.cancel")}
        </Button>
        <Button tone="primary" type="submit" className="h-8" busy={busy}>
          {t("common.save")}
        </Button>
      </form>
    );
  return (
    <Line label={t("admin-pages-overview.name")} hint={o.hub.name_set ? t("admin-pages-overview.name_hint") : t("admin-pages-overview.name_default_hint", { name: o.hub.default_name })}>
      <span className="text-[13px] font-medium text-ink">{o.hub.name}</span>
      <IconButton icon="write" label={t("admin-pages-overview.rename")} size={26} iconSize={13} onClick={() => (setValue(o.hub.name_set ? o.hub.name : ""), setEditing(true))} />
    </Line>
  );
}

export function Endpoints({ list }: { list: Endpoint[] }) {
  if (!list.length) return <div className="px-4 py-3 text-[12.5px] text-ink-3">{t("admin-pages-overview.endpoints_empty")}</div>;
  return (
    <>
      {list.map((e) => (
        <div key={e.url} className="flex items-center gap-3 px-4 py-2">
          <Tag tone={e.scheme === "https" ? "green" : "gray"}>{e.scheme === "https" ? "HTTPS" : "HTTP"}</Tag>
          <CopyValue value={e.url} className="min-w-0 flex-1" />
          <span className="hidden shrink-0 text-[11.5px] text-ink-3 sm:inline">{endpointKindLabel(e.kind)}</span>
        </div>
      ))}
    </>
  );
}

function Doctor() {
  const [report, setReport] = useState<DoctorReport | null>(null);
  const [busy, setBusy] = useState(false);
  const run = async () => {
    setBusy(true);
    try {
      setReport(await adminApi<DoctorReport>("/doctor", "POST"));
    } catch (e) {
      toast(errorText(e), "error");
    } finally {
      setBusy(false);
    }
  };
  const g = report ? groupDoctor(report.items) : null;
  return (
    <Card
      title={t("admin-pages-overview.doctor")}
      right={
        <Button className="h-7 px-2.5" busy={busy} onClick={run}>
          {!busy && <AdminIcon name="radar" size={13} />}
          {report ? t("admin-pages-overview.doctor_rerun") : t("admin-pages-overview.doctor_run")}
        </Button>
      }
      footer={report ? t("admin-pages-overview.doctor_ran", { time: dateTime(report.ran_at) }) : t("admin-pages-overview.doctor_footer")}
    >
      {!g ? (
        <div className="px-4 py-3 text-[12.5px] text-ink-3">{t("admin-pages-overview.doctor_none")}</div>
      ) : (
        <>
          {[...g.problem, ...g.ok, ...g.info].map((it, i) => (
            <div key={i} className="flex items-start gap-3 px-4 py-2.5">
              <span className="mt-1.5">
                <Dot tone={it.level === "problem" ? "red" : it.level === "ok" ? "green" : "gray"} />
              </span>
              <div className="min-w-0 flex-1">
                <div className={cx("text-[13px]", it.level === "problem" ? "font-medium text-ink" : "text-ink")}>{it.item}</div>
                {/* A detail's later lines are the item's list (addresses, devices, firewall rules): one per line. */}
                <div className="mt-0.5 text-[12px] leading-relaxed break-words whitespace-pre-line text-ink-2">{it.detail}</div>
              </div>
            </div>
          ))}
          {report!.next.length > 0 && (
            <div className="bg-inset px-4 py-3">
              <div className="mb-1.5 text-[12px] font-medium text-ink-2">{t("admin-pages-overview.next_steps")}</div>
              <ol className="list-decimal space-y-1 pl-4 text-[12px] leading-relaxed text-ink-2 marker:text-ink-3">
                {report!.next.map((s, i) => (
                  <li key={i} className="break-words">
                    {s}
                  </li>
                ))}
              </ol>
            </div>
          )}
        </>
      )}
    </Card>
  );
}
