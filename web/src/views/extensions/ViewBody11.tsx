// UI version 11's interactive scene, loaded only with the first scene. Canvas draws the extension's geometry; DOM
// buttons provide accessible pins and design-system controls. Camera state is scoped by extension owner and item key.
import { useContext, useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import { createPortal } from "react-dom";
import { t } from "../../i18n";
import { useCloseOnBack } from "../../lib/back-close";
import { ownerContext } from "../../lib/extension-owner";
import { toneOf, type UIAction, type UIItem } from "../../lib/extension-ui";
import { hue } from "../../lib/extension-ui-v6";
import type { ScenePin } from "../../lib/extension-wire";
import { cx, IconButton, Spinner } from "../../ui/atoms";
import { Icon } from "../../ui/icons";
import { useDark } from "../../ui/use-dark";
import { useEscapeClose, useModalFocus } from "../../ui/overlay";
import { glyphOf } from "./ViewBody6";
import { ROW_ICON } from "./ViewBody";
import { drawScene, sceneCamera, sceneGeometry, sceneProjection, type SceneCameraState } from "./scene-geometry";

interface KeptCamera { camera: SceneCameraState; touched: boolean }
const STORES = new Map<string, KeptCamera>();
const STORE_CAP = 64;
const TONE_HUE: Record<string, string> = { positive: "green", caution: "orange", danger: "red" };
const clamp = (v: number, lo: number, hi: number) => Math.min(hi, Math.max(lo, v));
const cameraStore = (scope: string, key: string): KeptCamera | undefined => {
  const id = `${scope}\u0000${key}`;
  const found = STORES.get(id);
  if (found) { STORES.delete(id); STORES.set(id, found); }
  return found;
};
const keepCamera = (scope: string, key: string, kept: KeptCamera) => {
  const id = `${scope}\u0000${key}`;
  STORES.delete(id);
  STORES.set(id, kept);
  if (STORES.size > STORE_CAP) STORES.delete(STORES.keys().next().value!);
};
export const forgetSceneStores = () => STORES.clear();

interface ViewProps {
  item: UIItem;
  camera: SceneCameraState;
  interactive: boolean;
  busy: string;
  onPress: (a: UIAction, destructive?: boolean) => void;
  canPress?: (a: UIAction) => boolean;
  onCamera: (camera: SceneCameraState) => void;
  onReset: () => void;
  full?: boolean;
  onFull?: () => void;
  opener?: React.RefObject<HTMLButtonElement | null>;
}

function SceneView({ item, camera, interactive, busy, onPress, canPress, onCamera, onReset, full, onFull, opener }: ViewProps) {
  const stage = useRef<HTMLDivElement>(null);
  const canvas = useRef<HTMLCanvasElement>(null);
  const dark = useDark();
  const [size, setSize] = useState({ w: 800, h: interactive ? 450 : 200 });
  const shapes = item.shapes ?? [];
  const shapeKey = JSON.stringify(shapes);
  const pins = item.pins ?? [];
  const labels = item.labels ?? [];
  // State-only redraws reuse geometry when the shapes' content is unchanged.
  // eslint-disable-next-line react-hooks/exhaustive-deps
  const geometry = useMemo(() => sceneGeometry(shapes), [shapeKey]);
  const project = useMemo(() => sceneProjection(geometry.bounds, camera, size.w, size.h), [geometry, camera, size]);

  useLayoutEffect(() => {
    const el = stage.current;
    if (!el) return;
    const measure = () => {
      const w = Math.round(el.clientWidth), h = Math.round(el.clientHeight);
      if (w > 0 && h > 0) setSize((old) => old.w === w && old.h === h ? old : { w, h });
    };
    measure();
    if (typeof ResizeObserver === "undefined") return;
    const observer = new ResizeObserver(measure);
    observer.observe(el);
    return () => observer.disconnect();
  }, []);

  useLayoutEffect(() => {
    const el = canvas.current;
    const ctx = el?.getContext("2d");
    if (!el || !ctx) return;
    const ratio = Math.min(2, globalThis.devicePixelRatio || 1);
    el.width = Math.round(size.w * ratio);
    el.height = Math.round(size.h * ratio);
    ctx.setTransform(ratio, 0, 0, ratio, 0, 0);
    drawScene(ctx, size.w, size.h, geometry, pins, camera, dark);
  }, [size, geometry, pins, camera, dark]);

  const points = useRef(new Map<number, { x: number; y: number }>());
  const pinch = useRef<{ distance: number; zoom: number } | null>(null);
  const distance = () => {
    const p = [...points.current.values()];
    return p.length >= 2 ? Math.hypot(p[0].x - p[1].x, p[0].y - p[1].y) : 0;
  };
  const zoom = (factor: number) => onCamera({ ...camera, zoom: clamp(camera.zoom * factor, 0.5, 4) });

  useEffect(() => {
    const el = stage.current;
    if (!el || !interactive) return;
    const wheel = (event: WheelEvent) => {
      event.preventDefault();
      onCamera({ ...camera, zoom: clamp(camera.zoom * Math.exp(-event.deltaY * 0.001), 0.5, 4) });
    };
    el.addEventListener("wheel", wheel, { passive: false });
    return () => el.removeEventListener("wheel", wheel);
  }, [interactive, camera, onCamera]);

  const pinColor = (pin: ScenePin) => pin.color ? hue(pin.color) : pin.tone ? hue(TONE_HUE[toneOf(pin.tone)]) : undefined;
  const placedPins = pins.map((pin) => ({ pin, at: project(pin.at[0], pin.at[1], pin.height ?? 0) }));
  const minPinDepth = Math.min(...placedPins.map(({ at }) => at.depth));
  const controlButtons = interactive && (
    <div className="absolute right-3 bottom-3 z-[100] flex items-center gap-0.5 rounded-full border border-line bg-surface p-1 shadow-card">
      <IconButton icon="minus" label={t("extensions-viewbody.graph_zoom_out")} size={32} onClick={() => zoom(0.8)} />
      <IconButton icon="plus" label={t("extensions-viewbody.graph_zoom_in")} size={32} onClick={() => zoom(1.25)} />
      <IconButton icon="fit" label={t("extensions-viewbody.scene_reset")} size={32} onClick={onReset} />
      {!full && <IconButton icon="expand" label={t("extensions-viewbody.graph_full_screen")} size={32} buttonRef={opener} onClick={onFull} />}
    </div>
  );

  return (
    <div className={cx("relative", full && "flex h-full min-h-0 flex-col")}>
      <div
        ref={stage}
        role="group"
        aria-label={item.alt}
        tabIndex={interactive ? 0 : undefined}
        data-scene-stage=""
        data-camera-yaw={camera.yaw}
        data-camera-pitch={camera.pitch}
        data-camera-zoom={camera.zoom}
        className={cx("relative w-full overflow-hidden rounded-card bg-inset", full ? "min-h-0 flex-1" : interactive ? "graph-canvas" : "graph-preview", interactive && "touch-none focus-visible:outline-2 focus-visible:outline-accent")}
        onKeyDown={(event) => {
          if (!interactive || event.target !== event.currentTarget) return;
          const moves: Record<string, [number, number]> = { ArrowLeft: [-5, 0], ArrowRight: [5, 0], ArrowUp: [0, 5], ArrowDown: [0, -5] };
          if (moves[event.key]) {
            event.preventDefault();
            const [yaw, pitch] = moves[event.key];
            onCamera({ ...camera, yaw: camera.yaw + yaw, pitch: clamp(camera.pitch + pitch, 10, 90) });
          } else if (event.key === "+" || event.key === "=") { event.preventDefault(); zoom(1.25); }
          else if (event.key === "-") { event.preventDefault(); zoom(0.8); }
          else if (event.key === "0") { event.preventDefault(); onReset(); }
        }}
        onPointerDown={(event) => {
          if (!interactive || (event.target as Element).closest("button")) return;
          stage.current?.focus({ preventScroll: true });
          stage.current?.setPointerCapture(event.pointerId);
          points.current.set(event.pointerId, { x: event.clientX, y: event.clientY });
          if (points.current.size === 2) pinch.current = { distance: distance(), zoom: camera.zoom };
        }}
        onPointerMove={(event) => {
          if (!interactive || !points.current.has(event.pointerId)) return;
          const previous = points.current.get(event.pointerId)!;
          points.current.set(event.pointerId, { x: event.clientX, y: event.clientY });
          if (points.current.size >= 2 && pinch.current) {
            const current = distance();
            if (current > 0 && pinch.current.distance > 0) onCamera({ ...camera, zoom: clamp(pinch.current.zoom * current / pinch.current.distance, 0.5, 4) });
          } else if (points.current.size === 1) {
            onCamera({ ...camera, yaw: camera.yaw + (event.clientX - previous.x) * 0.45, pitch: clamp(camera.pitch + (event.clientY - previous.y) * 0.3, 10, 90) });
          }
        }}
        onPointerUp={(event) => { points.current.delete(event.pointerId); if (points.current.size < 2) pinch.current = null; }}
        onPointerCancel={(event) => { points.current.delete(event.pointerId); pinch.current = null; }}
      >
        <span data-scene-alt="" className="sr-only">{item.alt}</span>
        {labels.map((label, i) => <span key={`sr-${i}`} data-scene-label="" className="sr-only">{label.text}</span>)}
        <canvas ref={canvas} data-scene-canvas="" aria-hidden="true" className="block h-full w-full" />
        {labels.map((label, i) => {
          const at = project(label.at[0], label.at[1], label.height ?? 0);
          return <span key={i} aria-hidden="true" className="pointer-events-none absolute z-[1] -translate-x-1/2 -translate-y-1/2 rounded-full border border-line bg-surface px-2 py-0.5 text-submeta whitespace-nowrap text-ink-2 shadow-card" style={{ left: at.x, top: at.y }}>{label.text}</span>;
        })}
        {placedPins.map(({ pin, at }) => {
          const color = pinColor(pin);
          const glyph = glyphOf(pin.icon, ROW_ICON);
          const action = interactive ? pin.action : undefined;
          const label = [pin.label, pin.state].filter(Boolean).join(t("extensions-viewbody.read_sep"));
          const content = <>
            {busy === action?.id ? <Spinner size={12} /> : glyph ? <Icon name={glyph} size={13} style={color ? { color } : undefined} /> : <span aria-hidden="true" className="size-1.5 rounded-full" style={{ background: color ?? "var(--ink-3)" }} />}
            <span className="truncate">{pin.label}</span>
            {pin.state && <span className="shrink-0 text-ink-3">{pin.state}</span>}
          </>;
          const common = {
            "data-scene-pin": pin.id,
            "aria-label": label,
            className: "touch-hit absolute z-[2] flex max-w-44 -translate-x-1/2 -translate-y-full items-center gap-1.5 rounded-full border border-line bg-surface px-2 py-1 text-left text-submeta whitespace-nowrap text-ink shadow-card disabled:opacity-60",
            style: { left: clamp(at.x, 90, Math.max(90, size.w - 90)), top: clamp(at.y, 44, Math.max(44, size.h - 12)), zIndex: 2 + Math.round((at.depth - minPinDepth) * 100) },
          } as const;
          return action ? (
            <button key={pin.id} type="button" {...common} aria-busy={busy === action.id || undefined} disabled={(!!busy && busy !== action.id) || (canPress ? !canPress(action) : false)} onClick={() => onPress(action)}>{content}</button>
          ) : (
            <span key={pin.id} role="group" tabIndex={interactive ? 0 : undefined} {...common}>{content}</span>
          );
        })}
        {controlButtons}
      </div>
      {item.caption && <p className={cx("px-1 pt-1.5 text-submeta text-ink-3", full && "shrink-0")}>{item.caption}</p>}
    </div>
  );
}

export function SceneCanvas({ item, compact, busy, onPress, canPress }: {
  item: UIItem;
  compact?: boolean;
  busy: string;
  onPress: (a: UIAction, destructive?: boolean) => void;
  canPress?: (a: UIAction) => boolean;
}) {
  const scope = useContext(ownerContext) ?? (typeof location !== "undefined" ? location.pathname : "");
  const key = item.key ?? "";
  const local = useRef<KeptCamera | null>(null);
  const initial = (): KeptCamera => key && cameraStore(scope, key) || local.current || { camera: sceneCamera(item.camera), touched: false };
  const [kept, setKept] = useState<KeptCamera>(initial);
  const [full, setFull] = useState(false);
  const dialog = useRef<HTMLDivElement>(null);
  const opener = useRef<HTMLButtonElement>(null);
  const wasFull = useRef(false);
  if (full) wasFull.current = true;

  useEffect(() => {
    setKept(initial());
    // The owner/key chooses a different persisted camera.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [scope, key]);
  useEffect(() => {
    if (!kept.touched) setKept({ camera: sceneCamera(item.camera), touched: false });
    // An untouched camera follows the extension's default; a touched one is the person's.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [item.camera?.yaw, item.camera?.pitch, item.camera?.zoom]);
  useLayoutEffect(() => {
    if (full || !wasFull.current) return;
    wasFull.current = false;
    if ((!document.activeElement || document.activeElement === document.body) && opener.current?.isConnected) opener.current.focus({ preventScroll: true });
  }, [full]);
  useModalFocus(dialog, full);
  useEscapeClose(full, () => setFull(false));
  useCloseOnBack(full, () => setFull(false));

  const update = (camera: SceneCameraState) => {
    const next = { camera: sceneCamera(camera), touched: true };
    local.current = next;
    setKept(next);
    if (key) keepCamera(scope, key, next);
  };
  const reset = () => {
    const next = { camera: sceneCamera(item.camera), touched: false };
    local.current = next;
    setKept(next);
    if (key) keepCamera(scope, key, next);
  };
  const view = (interactive: boolean, isFull = false) => (
    <SceneView item={item} camera={kept.camera} interactive={interactive} busy={busy} onPress={onPress} canPress={canPress} onCamera={update} onReset={reset} full={isFull} onFull={() => setFull(true)} opener={opener} />
  );

  if (full) return (
    <>
      {compact && <div aria-hidden="true" className="px-4 py-3"><div className="graph-preview" /></div>}
      {createPortal(
        <div ref={dialog} role="dialog" aria-modal="true" aria-label={t("extensions-viewbody.scene")} tabIndex={-1} className="fixed inset-0 z-50 flex flex-col bg-page p-2 outline-none sm:p-4">
          <div className="flex shrink-0 justify-end"><IconButton icon="x" label={t("extensions-viewbody.graph_close")} size={36} onClick={() => setFull(false)} /></div>
          <div className="min-h-0 flex-1">{view(true, true)}</div>
        </div>, document.body,
      )}
    </>
  );
  if (compact) return (
    <div className="px-4 py-3">
      <button type="button" ref={opener} aria-label={t("extensions-viewbody.graph_full_screen")} title={t("extensions-viewbody.graph_full_screen")} onClick={() => setFull(true)} className="graph-preview block w-full cursor-zoom-in overflow-hidden rounded-card">
        {view(false)}
      </button>
    </div>
  );
  return view(true);
}
