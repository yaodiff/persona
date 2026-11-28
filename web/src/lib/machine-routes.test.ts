import { expect, it } from "vitest";
import { appPath } from "./deep-links";
import { href, parse } from "./router";

it.each(["/machines", "/machines/Computer%20B", "/machines/b/work", "/machines/b/files",
  "/machines/b/storage?mode=reclaim", "/machines/b/diagnostics", "/machines/b/procs", "/machines/b/services",
  "/machines/b/toolchain", "/machines/b/network", "/machines/b/management", "/machines/b/config",
  ...["1h", "6h", "24h"].flatMap(range => [`/machines/b?range=${range}`, `/machines/b/monitoring?range=${range}`]),
])("preserves the machine address %s", path => {
  const [pathname, query] = path.split("?");
  expect(href(parse(pathname!, query))).toBe(path);
  expect(appPath("persona://open" + path)).toBe(path);
});

it.each(["/machines/b/missing", "/machines/b/files/extra"])("rejects an unknown view at %s", path => {
  expect(parse(path).name).toBe("not-found");
  expect(appPath("persona://open" + path)).toBeNull();
});

it.each([
  ["/machines/Computer%20B/fs?dir=%2FProjects%2FA%20%26%20B", "/machines/Computer%20B/files?path=%2FProjects%2FA%20%26%20B"],
  ["/machines/b/files?path=C%3A%5CWork", "/machines/b/files?path=C%3A%5CWork"],
  ["/machines/b/cleanup", "/machines/b/storage?mode=reclaim"],
  ["/machines/b/storage/cleanup", "/machines/b/storage?mode=reclaim"],
])("maps the incoming address %s to the same computer and target", (input, canonical) => {
  const [path, query] = input.split("?");
  expect(href(parse(path!, query))).toBe(canonical);
  expect(appPath("persona://open" + input)).toBe(canonical);
});
