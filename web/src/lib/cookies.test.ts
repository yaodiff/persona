import { describe, expect, it } from "vitest";
import { parseCookies, picked, siteExpired, siteList, siteOf, sitesOf, siteSummary, SITES_IN_PLACE } from "./cookies";

describe("parseCookies", () => {
  it("reads Netscape cookies.txt, with #HttpOnly_ lines", () => {
    const txt = [
      "# Netscape HTTP Cookie File",
      "",
      ".example.com\tTRUE\t/\tTRUE\t2000000000\tsid\tabc\tdef",
      "#HttpOnly_www.example.org\tFALSE\t/app\tFALSE\t0\tpref\tx",
      "broken line",
    ].join("\n");
    const r = parseCookies(txt);
    expect(r.format).toBe("netscape");
    expect(r.bad).toBe(1);
    expect(r.cookies).toEqual([
      { name: "sid", value: "abc\tdef", domain: ".example.com", path: "/", secure: true, expires: 2000000000, http_only: false, host_only: false },
      { name: "pref", value: "x", domain: "www.example.org", path: "/app", secure: false, expires: undefined, http_only: true, host_only: true },
    ]);
  });

  it("reads Cookie-Editor / EditThisCookie JSON", () => {
    const r = parseCookies(
      JSON.stringify([
        { domain: ".example.com", expirationDate: 2000000000.5, hostOnly: false, httpOnly: true, name: "sid", path: "/", sameSite: "lax", secure: true, session: false, value: "v" },
        { domain: "shop.example.com", hostOnly: true, name: "cart", path: "/", sameSite: "no_restriction", secure: true, session: true, value: "1" },
        { domain: "x.test", expires: 2000000000000, name: "ms", value: "1" }, // milliseconds
        { name: "no domain", value: "1" },
      ]),
    );
    expect(r).toMatchObject({ format: "json", bad: 1 });
    expect(r.cookies[0]).toMatchObject({ name: "sid", expires: 2000000000, http_only: true, same_site: "Lax", secure: true, host_only: false });
    expect(r.cookies[1]).toMatchObject({ name: "cart", expires: undefined, same_site: "None", host_only: true });
    expect(r.cookies[2].expires).toBe(2000000000);
    expect(parseCookies(JSON.stringify({ cookies: [{ name: "a", value: "b", domain: "c.d" }] })).cookies).toHaveLength(1);
  });

  it("explains what is no cookie file", () => {
    expect(() => parseCookies("{oops")).toThrow("无法解析该文件");
    expect(() => parseCookies("[]")).toThrow("不包含任何 cookie");
    expect(() => parseCookies("hello\nworld")).toThrow("cookies.txt");
  });
});

describe("sites", () => {
  it("groups by site, counts the expired apart, and picks the ticked", () => {
    expect([siteOf(".www.example.com"), siteOf("mail.example.co.uk"), siteOf("a.b.example.com.cn"), siteOf("localhost"), siteOf("198.51.100.7")]).toEqual([
      "example.com",
      "example.co.uk",
      "example.com.cn",
      "localhost",
      "198.51.100.7",
    ]);
    const now = 1_900_000_000;
    const sites = sitesOf(
      [
        { name: "a", value: "1", domain: ".example.com", expires: 2_000_000_000 },
        { name: "b", value: "2", domain: "www.example.com" },
        { name: "old", value: "3", domain: "example.com", expires: 1_000 },
        { name: "c", value: "4", domain: "shop.example.org", expires: 1_950_000_000 },
      ],
      now,
    );
    expect(sites.map((s) => [s.site, s.cookies.length, s.expired, s.expires])).toEqual([
      ["example.com", 2, 1, 2_000_000_000],
      ["example.org", 1, 0, 1_950_000_000],
    ]);
    expect(picked(sites, new Set(["example.org"])).map((c) => c.name)).toEqual(["c"]);
  });
});

describe("the sites on a computer's page (#5821)", () => {
  const now = Date.parse("2026-11-12T08:00:00Z");
  const sites = [
    { domain: "maple.example", count: 2, expires: "2027-01-01T00:00:00Z" },
    { domain: "aspen.example", count: 1 },
    { domain: "cedar.example", count: 4, expires: "2026-11-01T00:00:00Z" },
    { domain: "birch.example", count: 3, expires: "2026-12-01T00:00:00Z" },
  ];

  it("lists by name, or the soonest to expire first with the expired on top and browser-only ones last", () => {
    expect(siteList(sites, "", "name").map((s) => s.domain)).toEqual(["aspen.example", "birch.example", "cedar.example", "maple.example"]);
    expect(siteList(sites, "", "expiry").map((s) => s.domain)).toEqual(["cedar.example", "birch.example", "maple.example", "aspen.example"]);
    expect(sites[0]!.domain).toBe("maple.example"); // the input is left as it was
  });

  it("filters by domain in any case, ignoring the spaces around the query", () => {
    expect(siteList(sites, " AR ", "name").map((s) => s.domain)).toEqual(["cedar.example"]);
    expect(siteList(sites, "example", "name")).toHaveLength(4);
    expect(siteList(sites, "nothing", "name")).toEqual([]);
  });

  it("sums up the count, the first expiry still to come and how many have expired", () => {
    expect(siteSummary(sites, now)).toEqual({ count: 4, first: Date.parse("2026-12-01T00:00:00Z"), expired: 1 });
    expect(siteSummary([{ domain: "a.example", count: 1 }], now)).toEqual({ count: 1, first: undefined, expired: 0 });
    expect(siteExpired(sites[2]!, now)).toBe(true);
    expect(siteExpired(sites[1]!, now)).toBe(false);
    expect(SITES_IN_PLACE).toBe(3);
  });
});
