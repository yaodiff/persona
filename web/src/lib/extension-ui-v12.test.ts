import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";
import {
  isGraphLink, MAP_LINK_CAP, MAP_MARKER_CAP, MAP_POINT_CAP, MAP_ROUTE_CAP,
  readMap, readView, UI_ICONS, UI_VERSION, uiContext, uiIcon, type UIItem,
} from "./extension-ui";

const example = (name: string) => JSON.parse(readFileSync(new URL(`../../../schema/extension/examples/${name}`, import.meta.url), "utf8"));
const mapOf = (raw: unknown) => readView({ sections: [{ items: [raw] }] }).sections[0]?.items[0] as UIItem | undefined;

describe("UI 12: map component reading and validation", () => {
  it("asks for UI version 12 or later, draws scenes too (nothing unsupported), and includes map icon", () => {
    expect(UI_VERSION).toBe(13);
    expect(uiContext().ui_version).toBe(13);
    // scenes render since #13172 (ViewBody11.tsx), so nothing is reported unsupported
    expect(uiContext().unsupported).toBeUndefined();
    expect(UI_ICONS).toContain("map");
    expect(uiIcon("map")).toBe("map");
  });

  it("parses View.v12-map-route.json completely", () => {
    const view = readView(example("View.v12-map-route.json"));
    expect(view.sections).toHaveLength(1);
    const map = view.sections[0]?.items[0];
    expect(map?.type).toBe("map");
    expect(map?.key).toBe("route-home-office");
    expect(map?.title).toBe("Home to Office");
    expect(map?.alt).toContain("Driving route");
    expect(map?.markers).toHaveLength(3);
    expect(map?.markers?.[1]).toMatchObject({ id: "stop-1", kind: "waypoint", color: "amber", detail: "Fast charger available near exit 12" });
    expect(map?.markers?.[2]?.action).toEqual({ id: "focus", values: { stop: "dest" } });
    expect(map?.routes).toHaveLength(2);
    expect(map?.routes?.[0]).toMatchObject({ id: "r-fastest", mode: "driving", selected: true, tone: "positive" });
    expect(map?.links).toHaveLength(1);
    expect(map?.show_list).toBe(true);
  });

  it("drops map without required alt or any geometry", () => {
    expect(readMap({ type: "map", markers: [{ id: "a", lat: 1, lon: 2, label: "A" }] })).toBeUndefined();
    expect(readMap({ type: "map", alt: "   ", viewport: { center: { lat: 0, lon: 0 } } })).toBeUndefined();
    expect(readMap({ type: "map", alt: "No geometry" })).toBeUndefined();
    expect(mapOf({ type: "map", alt: "No geometry" })).toBeUndefined();
    expect(mapOf({ type: "map", alt: "Valid viewport only", viewport: { center: { lat: 39.9, lon: 116.4 }, span_lat: 0.05 } })?.type).toBe("map");
  });

  it("validates bounds, duplicate IDs, mutually exclusive tone/color, route points, and link safety", () => {
    const got = readMap({
      type: "map",
      alt: "Filtered map",
      viewport: { center: { lat: 95, lon: 10 } },
      markers: [
        { id: "m1", lat: 39.9, lon: 116.4, label: "Valid", tone: "positive", color: "teal", kind: "origin" },
        { id: "m1", lat: 40, lon: 116.5, label: "Duplicate ID" },
        { id: "bad-lat", lat: 91, lon: 0, label: "Bad" },
        { id: "bad-lon", lat: 0, lon: -181, label: "Bad" },
        { id: "nan", lat: Number.NaN, lon: 0, label: "Bad" },
        { id: "nolabel", lat: 10, lon: 10, label: "" },
        { id: "m2", lat: 39.95, lon: 116.45, label: "Bad role", kind: "spaceship", color: "invalid", tone: "bogus" },
      ],
      routes: [
        { id: "r1", points: [{ lat: 39.9, lon: 116.4 }, { lat: 39.95, lon: 116.45 }], mode: "walking", tone: "caution", color: "red", distance_meters: 1200, duration_seconds: 900 },
        { id: "r1", points: [{ lat: 0, lon: 0 }, { lat: 1, lon: 1 }] },
        { id: "too-short", points: [{ lat: 0, lon: 0 }] },
        { id: "out-of-bounds", points: [{ lat: 0, lon: 0 }, { lat: 99, lon: 0 }] },
      ],
      links: [
        { title: "HTTP", url: "http://example.com" },
        { title: "Creds", url: "https://user:pass@example.com" },
        { title: "Space", url: " https://example.com" },
        { title: "Good", url: "https://maps.example.com/dir" },
      ],
    });
    expect(got?.viewport).toBeUndefined();
    expect(got?.markers?.map((m) => m.id)).toEqual(["m1", "m2"]);
    expect(got?.markers?.[0]?.tone).toBe("positive");
    expect(got?.markers?.[0]?.color).toBeUndefined();
    expect(got?.markers?.[1]?.kind).toBeUndefined();
    expect(got?.markers?.[1]?.tone).toBeUndefined();
    expect(got?.markers?.[1]?.color).toBeUndefined();
    expect(got?.routes?.map((r) => r.id)).toEqual(["r1"]);
    expect(got?.routes?.[0]?.tone).toBe("caution");
    expect(got?.routes?.[0]?.color).toBeUndefined();
    expect(got?.links).toEqual([{ title: "Good", url: "https://maps.example.com/dir" }]);
    expect(isGraphLink("https://maps.example.com/dir")).toBe(true);
  });

  it("enforces marker, route, cumulative point, and link caps", () => {
    const markers = Array.from({ length: MAP_MARKER_CAP + 15 }, (_, i) => ({
      id: `m-${i}`, lat: 39 + i * 0.0001, lon: 116, label: `Stop ${i}`,
    }));
    const links = Array.from({ length: MAP_LINK_CAP + 4 }, (_, i) => ({
      title: `Link ${i}`, url: `https://example.com/${i}`,
    }));
    const routes = Array.from({ length: MAP_ROUTE_CAP + 3 }, (_, i) => ({
      id: `r-${i}`,
      points: Array.from({ length: 300 }, (_, p) => ({ lat: 39 + p * 0.0001, lon: 116 + i * 0.001 })),
    }));
    const got = readMap({ type: "map", alt: "Capped map", markers, routes, links });
    expect(got?.markers).toHaveLength(MAP_MARKER_CAP);
    expect(got?.links).toHaveLength(MAP_LINK_CAP);
    // 6 routes * 300 points = 1800 <= 2000; 7th route of 300 points would hit 2100 > MAP_POINT_CAP
    expect(got?.routes).toHaveLength(6);
    expect(got?.routes?.reduce((n, r) => n + r.points.length, 0)).toBeLessThanOrEqual(MAP_POINT_CAP);
  });
});
