// UI version 11's scene geometry and Canvas drawing. The extension supplies shapes and content colors; the app owns
// projection, lighting and every control around them. No WebGL dependency: the lazy Canvas chunk draws the same
// orthographic basis as ExtensionSceneGeometry.swift.
import type { SceneCamera, ScenePin, SceneShape } from "../../lib/extension-wire";

export interface SceneCameraState { yaw: number; pitch: number; zoom: number }
export interface ScenePoint { x: number; y: number; h: number }
export interface ProjectedPoint { x: number; y: number; depth: number }
export type SceneProjection = (x: number, y: number, h?: number) => ProjectedPoint;

export interface Bounds { minX: number; maxX: number; minY: number; maxY: number; minH: number; maxH: number }
export interface Face { points: ScenePoint[]; color?: string; material: string; top: boolean; bevel?: boolean; normal?: [number, number] }
export interface SceneGeometry { bounds: Bounds; faces: Face[] }

const DEFAULTS = { matte: "#B8BDC5", glass: "#B8D8E8", metal: "#A9ADB5", screen: "#20242B", tile: "#D8CDBB", wood: "#B98955" };
const rad = (degrees: number) => degrees * Math.PI / 180;
const clamp = (v: number, lo: number, hi: number) => Math.min(hi, Math.max(lo, v));

export function sceneCamera(input?: SceneCamera): SceneCameraState {
  const yaw = ((input?.yaw ?? 225) % 360 + 360) % 360;
  return { yaw, pitch: clamp(input?.pitch ?? 35, 10, 90), zoom: clamp(input?.zoom ?? 1, 0.5, 4) };
}

export function sceneBounds(shapes: readonly SceneShape[]): Bounds {
  let minX = Infinity, maxX = -Infinity, minY = Infinity, maxY = -Infinity, minH = Infinity, maxH = -Infinity;
  const include = (x: number, y: number, bottom: number, top: number) => {
    minX = Math.min(minX, x); maxX = Math.max(maxX, x);
    minY = Math.min(minY, y); maxY = Math.max(maxY, y);
    minH = Math.min(minH, bottom, top); maxH = Math.max(maxH, bottom, top);
  };
  for (const shape of shapes) {
    if (shape.kind === "box") {
      const hw = Math.abs(shape.size[0]) / 2, hd = Math.abs(shape.size[1]) / 2;
      const c = Math.abs(Math.cos(rad(shape.yaw ?? 0))), s = Math.abs(Math.sin(rad(shape.yaw ?? 0)));
      const ex = hw * c + hd * s, ey = hw * s + hd * c;
      include(shape.center[0] - ex, shape.center[1] - ey, shape.bottom, shape.top);
      include(shape.center[0] + ex, shape.center[1] + ey, shape.bottom, shape.top);
    } else if (shape.kind === "cylinder") {
      include(shape.center[0] - shape.radius, shape.center[1] - shape.radius, shape.bottom, shape.top);
      include(shape.center[0] + shape.radius, shape.center[1] + shape.radius, shape.bottom, shape.top);
    } else {
      for (const p of shape.points) include(p[0], p[1], shape.bottom, shape.top);
    }
  }
  if (!Number.isFinite(minX)) return { minX: -0.5, maxX: 0.5, minY: -0.5, maxY: 0.5, minH: 0, maxH: 1 };
  if (maxX - minX < 0.01) { minX -= 0.05; maxX += 0.05; }
  if (maxY - minY < 0.01) { minY -= 0.05; maxY += 0.05; }
  if (maxH - minH < 0.005) maxH = minH + 0.05;
  return { minX, maxX, minY, maxY, minH, maxH };
}

export function sceneProjection(bounds: Bounds, input: SceneCameraState, width: number, height: number): SceneProjection {
  const camera = sceneCamera(input);
  const psi = rad(camera.yaw), phi = rad(camera.pitch);
  const sinPsi = Math.sin(psi), cosPsi = Math.cos(psi), sinPhi = Math.sin(phi), cosPhi = Math.cos(phi);
  const cx = (bounds.minX + bounds.maxX) / 2, cy = (bounds.minY + bounds.maxY) / 2, ch = (bounds.minH + bounds.maxH) / 2;
  const hx = (bounds.maxX - bounds.minX) / 2, hy = (bounds.maxY - bounds.minY) / 2, hh = (bounds.maxH - bounds.minH) / 2;
  const halfU = Math.max(0.25, Math.abs(sinPsi) * hx + Math.abs(cosPsi) * hy);
  const halfV = (Math.abs(cosPsi) * hx + Math.abs(sinPsi) * hy) * Math.abs(sinPhi) + hh * Math.abs(cosPhi);
  const aspect = Math.max(width, 1) / Math.max(height, 1);
  const scale = Math.max(halfV, halfU / Math.max(aspect, 0.1)) * 1.12 / camera.zoom;
  const ppm = Math.max(height, 1) / (2 * scale);
  return (x, y, h = 0) => {
    const rx = x - cx, ry = y - cy, rh = h - ch;
    const u = -sinPsi * rx + cosPsi * ry;
    const v = cosPhi * rh - sinPhi * (cosPsi * rx + sinPsi * ry);
    const depth = cosPhi * (cosPsi * rx + sinPsi * ry) + sinPhi * rh;
    return { x: width / 2 + u * ppm, y: height / 2 - v * ppm, depth };
  };
}

function boxRing(shape: Extract<SceneShape, { kind: "box" }>) {
  const hw = shape.size[0] / 2, hd = shape.size[1] / 2;
  const r = clamp(shape.round ?? 0, 0, Math.min(hw, hd));
  const out: [number, number][] = [];
  const normals: [number, number][] = [];
  if (!r) {
    out.push([hw, hd], [-hw, hd], [-hw, -hd], [hw, -hd]);
    normals.push([1, 1], [-1, 1], [-1, -1], [1, -1]);
  } else {
    const corners: [number, number, number][] = [[hw - r, hd - r, 0], [-hw + r, hd - r, 90], [-hw + r, -hd + r, 180], [hw - r, -hd + r, 270]];
    for (const [cx, cy, start] of corners) for (let i = 0; i <= 3; i++) {
      const a = rad(start + i * 30), nx = Math.cos(a), ny = Math.sin(a);
      out.push([cx + r * nx, cy + r * ny]);
      normals.push([nx, ny]);
    }
  }
  const a = rad(shape.yaw ?? 0), c = Math.cos(a), s = Math.sin(a);
  const turn = ([x, y]: [number, number]): [number, number] => [x * c - y * s, x * s + y * c];
  return {
    points: out.map((p) => { const [x, y] = turn(p); return [shape.center[0] + x, shape.center[1] + y] as [number, number]; }),
    normals: normals.map(turn),
  };
}

function boxPlan(shape: Extract<SceneShape, { kind: "box" }>): [number, number][] {
  return boxRing(shape).points;
}

function planOf(shape: SceneShape): [number, number][] {
  if (shape.kind === "box") return boxPlan(shape);
  if (shape.kind === "prism") return shape.points.map((p) => [p[0], p[1]]);
  return Array.from({ length: 24 }, (_, i) => {
    const a = i * Math.PI / 12;
    return [shape.center[0] + shape.radius * Math.cos(a), shape.center[1] + shape.radius * Math.sin(a)];
  });
}

export function sceneFaces(shapes: readonly SceneShape[]): Face[] {
  const faces: Face[] = [];
  for (const shape of shapes) {
    const box = shape.kind === "box" ? shape : undefined;
    const ring = box ? boxRing(box) : undefined;
    const plan = ring?.points ?? planOf(shape);
    const top = Math.max(shape.bottom, shape.top), bottom = Math.min(shape.bottom, shape.top);
    const base = { color: shape.color, material: shape.material || "matte" };
    const bevel = box ? Math.min(clamp(box.round ?? 0, 0, Math.min(box.size[0], box.size[1]) / 2), (top - bottom) * 0.42) : 0;
    const inner = box && ring && bevel > 0.003 ? ring.points.map((p, i) => {
      const inset = Math.min(bevel * 0.65, Math.min(box.size[0], box.size[1]) * 0.45);
      return [p[0] - ring.normals[i][0] * inset, p[1] - ring.normals[i][1] * inset] as [number, number];
    }) : undefined;
    const sideBottom = inner ? bottom + bevel : bottom;
    const sideTop = inner ? top - bevel : top;
    const cap = inner ?? plan;
    faces.push({ ...base, top: true, points: cap.map(([x, y]) => ({ x, y, h: top })) });
    for (let i = 0; i < plan.length; i++) {
      const a = plan[i], b = plan[(i + 1) % plan.length];
      const dx = b[0] - a[0], dy = b[1] - a[1], length = Math.hypot(dx, dy) || 1;
      faces.push({
        ...base, top: false, normal: [dy / length, -dx / length],
        points: [{ x: a[0], y: a[1], h: sideBottom }, { x: b[0], y: b[1], h: sideBottom }, { x: b[0], y: b[1], h: sideTop }, { x: a[0], y: a[1], h: sideTop }],
      });
      if (inner) {
        const ai = inner[i], bi = inner[(i + 1) % inner.length];
        faces.push({ ...base, top: false, bevel: true, points: [
          { x: a[0], y: a[1], h: sideTop }, { x: b[0], y: b[1], h: sideTop },
          { x: bi[0], y: bi[1], h: top }, { x: ai[0], y: ai[1], h: top },
        ] });
        faces.push({ ...base, top: false, points: [
          { x: ai[0], y: ai[1], h: bottom }, { x: bi[0], y: bi[1], h: bottom },
          { x: b[0], y: b[1], h: sideBottom }, { x: a[0], y: a[1], h: sideBottom },
        ] });
      }
    }
  }
  return faces;
}

export function sceneGeometry(shapes: readonly SceneShape[]): SceneGeometry {
  return { bounds: sceneBounds(shapes), faces: sceneFaces(shapes) };
}

function rgb(hex: string): [number, number, number] {
  const n = Number.parseInt(hex.slice(1), 16);
  return [(n >> 16) & 255, (n >> 8) & 255, n & 255];
}
function shade(hex: string, factor: number): string {
  const [r, g, b] = rgb(hex);
  return `rgb(${clamp(Math.round(r * factor), 0, 255)},${clamp(Math.round(g * factor), 0, 255)},${clamp(Math.round(b * factor), 0, 255)})`;
}
function path(ctx: CanvasRenderingContext2D, points: ProjectedPoint[]) {
  ctx.beginPath();
  points.forEach((p, i) => i ? ctx.lineTo(p.x, p.y) : ctx.moveTo(p.x, p.y));
  ctx.closePath();
}
function materialColor(face: Face): string {
  return face.color || DEFAULTS[face.material as keyof typeof DEFAULTS] || DEFAULTS.matte;
}

/** Draw one frame. Pins stay DOM controls above it; active lamp gradients are lighting only. */
export function drawScene(
  ctx: CanvasRenderingContext2D,
  width: number,
  height: number,
  geometry: SceneGeometry,
  pins: readonly ScenePin[],
  camera: SceneCameraState,
  dark: boolean,
): void {
  const project = sceneProjection(geometry.bounds, camera, width, height);
  ctx.clearRect(0, 0, width, height);
  const faces = geometry.faces.map((face) => {
    const points = face.points.map((p) => project(p.x, p.y, p.h));
    return { face, points, depth: points.reduce((sum, p) => sum + p.depth, 0) / points.length };
  }).sort((a, b) => a.depth - b.depth);
  for (const { face, points } of faces) {
    const color = materialColor(face);
    const directional = face.top || face.bevel ? 1.04 : 0.68 + 0.22 * Math.max(0, (face.normal?.[0] ?? 0) * -0.4 + (face.normal?.[1] ?? 0) * -0.7);
    const light = directional * (dark ? 0.82 : 1);
    const xs = points.map((p) => p.x), ys = points.map((p) => p.y);
    const minX = Math.min(...xs), maxX = Math.max(...xs), minY = Math.min(...ys), maxY = Math.max(...ys);
    path(ctx, points);
    ctx.globalAlpha = face.material === "glass" ? 0.42 : 1;
    if (face.material === "metal") {
      const gradient = ctx.createLinearGradient(minX, minY, maxX, maxY);
      gradient.addColorStop(0, shade(color, light * 0.72));
      gradient.addColorStop(0.48, shade(color, light * 1.22));
      gradient.addColorStop(1, shade(color, light * 0.78));
      ctx.fillStyle = gradient;
    } else ctx.fillStyle = shade(color, light * (face.material === "screen" ? 0.72 : 1));
    ctx.fill();
    ctx.globalAlpha = face.material === "glass" ? 0.7 : 1;
    ctx.strokeStyle = shade(color, Math.max(0.35, light * 0.58));
    ctx.lineWidth = face.top ? 1 : 0.65;
    ctx.lineJoin = "round";
    ctx.stroke();
    if (face.top && (face.material === "tile" || face.material === "wood")) {
      ctx.save();
      path(ctx, points);
      ctx.clip();
      ctx.strokeStyle = face.material === "tile" ? "rgba(0,0,0,0.12)" : "rgba(70,35,12,0.16)";
      ctx.lineWidth = 0.7;
      const step = face.material === "tile" ? 24 : 13;
      for (let x = Math.floor(minX / step) * step; x < maxX + step; x += step) {
        ctx.beginPath(); ctx.moveTo(x, minY); ctx.lineTo(x, maxY); ctx.stroke();
      }
      if (face.material === "tile") for (let y = Math.floor(minY / step) * step; y < maxY + step; y += step) {
        ctx.beginPath(); ctx.moveTo(minX, y); ctx.lineTo(maxX, y); ctx.stroke();
      }
      ctx.restore();
    }
    if (face.top && face.material === "screen") {
      ctx.strokeStyle = "rgba(255,255,255,0.28)";
      ctx.lineWidth = 1.2;
      ctx.beginPath(); ctx.moveTo(minX + 3, minY + 3); ctx.lineTo(maxX - 3, minY + 3); ctx.stroke();
    }
  }
  ctx.globalAlpha = 1;
  ctx.globalCompositeOperation = "screen";
  for (const pin of pins.filter((p) => p.light?.on).slice(0, 16)) {
    const p = project(pin.at[0], pin.at[1], pin.height ?? 0);
    const radius = 90 * camera.zoom;
    const gradient = ctx.createRadialGradient(p.x, p.y, 0, p.x, p.y, radius);
    gradient.addColorStop(0, pin.light?.warmth === "cool" ? "rgba(190,220,255,0.32)" : "rgba(255,210,125,0.36)");
    gradient.addColorStop(1, "rgba(0,0,0,0)");
    ctx.fillStyle = gradient;
    ctx.fillRect(p.x - radius, p.y - radius, radius * 2, radius * 2);
  }
  ctx.globalCompositeOperation = "source-over";
}
