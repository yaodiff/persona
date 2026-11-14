// The live screen's video in a real browser (#6558): the page's own wire (src/screen/video-wire.ts) and decoder setup
// (src/screen/video-bitstream.ts) decode the mock hub's two fixtures, HEVC and H.264 (scripts/mock/screen_video, made
// by VideoToolbox as Persona Screen encodes), every frame, with the browser's WebCodecs VideoDecoder:
//   (cd web && npx vite --port <web port>)
//   node web/e2e/screen-video-decode.mjs <web URL> [--executable <chromium>] [--require hevc,h264]
// A codec the browser says it can't decode (isConfigSupported) is reported and skipped, unless --require names it.
// Ends with one line: PASS, FAIL or TIMEOUT.
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { chromium } from "playwright-core";

const args = process.argv.slice(2);
const flag = (name) => {
  const i = args.indexOf(name);
  return i < 0 ? undefined : args.splice(i, 2)[1];
};
const executable = flag("--executable");
const required = (flag("--require") ?? "").split(",").filter(Boolean);
const [web] = args;
if (!web) {
  console.log("usage: node web/e2e/screen-video-decode.mjs <web URL> [--executable <chromium>] [--require hevc,h264]");
  process.exit(2);
}
const deadline = setTimeout(() => {
  console.log("TIMEOUT screen video decode: no verdict in 60 s");
  process.exit(1);
}, 60_000);

const fixtures = { hevc: "fixture.bin", h264: "fixture-h264.bin" };
const read = (name) => readFileSync(fileURLToPath(new URL(`../../scripts/mock/screen_video/${name}`, import.meta.url))).toString("base64");

const browser = await chromium.launch(executable ? { executablePath: executable } : { channel: "chrome" });
try {
  const page = await browser.newPage();
  const errors = [];
  page.on("pageerror", (e) => errors.push(String(e)));
  await page.goto(`${web}/e2e/screen-video-decode.html`);
  await page.waitForFunction(() => window.decodeReady === true, null, { timeout: 20_000 });
  const done = [];
  for (const [codec, file] of Object.entries(fixtures)) {
    const r = await page.evaluate((b64) => window.decodeFixture(b64), read(file));
    console.log(`${codec}: ${JSON.stringify(r)}`);
    if (r.supported === false) {
      if (required.includes(codec)) throw new Error(`${codec} is required but ${r.why}`);
      continue;
    }
    if (r.error) throw new Error(`${codec}: ${r.error}`);
    if (r.decoded !== r.frames) throw new Error(`${codec}: ${r.decoded} of ${r.frames} frames decoded`);
    if (r.size?.[0] !== 960 || r.size?.[1] !== 600) throw new Error(`${codec}: decoded at ${r.size}, not 960×600`);
    done.push(`${codec} (${r.codec}, ${r.frames} frames)`);
  }
  if (errors.length) throw new Error(`page errors: ${errors.join(" | ")}`);
  if (!done.length) throw new Error("this browser decoded neither fixture");
  console.log(`PASS screen video decode: ${done.join(", ")}`);
} catch (e) {
  console.log(`FAIL screen video decode: ${e instanceof Error ? e.message : e}`);
  process.exitCode = 1;
} finally {
  clearTimeout(deadline);
  await browser.close();
}
