import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";
import type { SceneShape } from "../../lib/extension-wire";
import { sceneBounds, sceneCamera, sceneFaces, sceneProjection } from "./scene-geometry";

const scene = JSON.parse(readFileSync("../schema/extension/examples/View.v11-scene-flat.json", "utf8")).sections[0].items[0];

describe("UI 11 scene geometry", () => {
  it("resolves and clamps the default camera", () => {
    expect(sceneCamera()).toEqual({ yaw: 225, pitch: 35, zoom: 1 });
    expect(sceneCamera({ yaw: -90, pitch: 2, zoom: 20 })).toEqual({ yaw: 270, pitch: 10, zoom: 4 });
  });

  it("builds top and side faces for every shape kind, including rounded box corners", () => {
    const shapes: SceneShape[] = [
      { kind: "box", center: [0, 0], size: [1, 2], bottom: 0, top: 1 },
      { kind: "box", center: [0, 0], size: [1, 2], round: 0.2, bottom: 0, top: 1 },
      { kind: "cylinder", center: [0, 0], radius: 1, bottom: 0, top: 1 },
      { kind: "prism", points: [[0, 0], [1, 0], [0, 1]], bottom: 0, top: 1 },
    ];
    const faces = sceneFaces(shapes);
    expect(faces.filter((f) => f.top)).toHaveLength(4);
    expect(faces).toHaveLength(5 + 49 + 25 + 4);
    expect(faces.filter((f) => f.bevel)).toHaveLength(16);
    expect(faces.filter((f) => f.bevel).every((f) => f.points.some((p) => p.h === 1))).toBe(true);
    expect(faces.filter((f) => f.normal).every((f) => Math.abs(Math.hypot(...f.normal!) - 1) < 1e-9)).toBe(true);
  });

  it("draws a wall-marked box as an ordinary box when the app has no cutaway", () => {
    const wall: SceneShape = { kind: "box", center: [0, 0], size: [4, 0.1], bottom: 0, top: 2.5, wall: true };
    expect(sceneFaces([wall])).toEqual(sceneFaces([{ ...wall, wall: false }]));
  });

  it("fits the merged flat example at every quarter turn without using pins for its bounds", () => {
    const bounds = sceneBounds(scene.shapes);
    expect(bounds.maxX).toBeLessThanOrEqual(8.1);
    for (const yaw of [0, 90, 180, 270, 360]) {
      const project = sceneProjection(bounds, { yaw, pitch: 35, zoom: 1 }, 800, 450);
      for (const face of sceneFaces(scene.shapes)) for (const point of face.points) {
        const p = project(point.x, point.y, point.h);
        expect(p.x).toBeGreaterThanOrEqual(0);
        expect(p.x).toBeLessThanOrEqual(800);
        expect(p.y).toBeGreaterThanOrEqual(0);
        expect(p.y).toBeLessThanOrEqual(450);
      }
    }
  });

  it("fits square-on and diagonal views from their projected extents", () => {
    const bounds = { minX: 0, maxX: 2, minY: 0, maxY: 8, minH: 0, maxH: 1 };
    const corners = [0, 2].flatMap((x) => [0, 8].flatMap((y) => [0, 1].map((h) => ({ x, y, h }))));
    const extents = (yaw: number, zoom = 1) => {
      const project = sceneProjection(bounds, { yaw, pitch: 35, zoom }, 800, 450);
      const points = corners.map((p) => project(p.x, p.y, p.h));
      const xs = points.map((p) => p.x), ys = points.map((p) => p.y);
      return { width: Math.max(...xs) - Math.min(...xs), height: Math.max(...ys) - Math.min(...ys), center: project(1, 4, 0.5) };
    };
    const square = extents(0), diagonal = extents(45), zoomed = extents(0, 2);
    expect(square.width).toBeCloseTo(714.285714, 6);
    expect(square.height).toBeCloseTo(175.562939, 6);
    expect(diagonal.width).toBeCloseTo(582.786301, 6);
    expect(diagonal.height).toBeCloseTo(401.785714, 6);
    expect(square.width).toBeGreaterThan(diagonal.width);
    expect(zoomed.width).toBeCloseTo(square.width * 2, 6);
    expect(zoomed.height).toBeCloseTo(square.height * 2, 6);
    expect(zoomed.center).toMatchObject({ x: 400, y: 225 });
  });

  it("keeps the target centered and zooms around it", () => {
    const bounds = sceneBounds(scene.shapes);
    const cx = (bounds.minX + bounds.maxX) / 2, cy = (bounds.minY + bounds.maxY) / 2, h = (bounds.minH + bounds.maxH) / 2;
    const one = sceneProjection(bounds, { yaw: 225, pitch: 35, zoom: 1 }, 800, 450);
    const two = sceneProjection(bounds, { yaw: 225, pitch: 35, zoom: 2 }, 800, 450);
    expect(one(cx, cy, h)).toMatchObject({ x: 400, y: 225 });
    expect(two(cx, cy, h)).toMatchObject({ x: 400, y: 225 });
    expect(two(bounds.maxX, cy, h).x - 400).toBeCloseTo(2 * (one(bounds.maxX, cy, h).x - 400));
  });
});
