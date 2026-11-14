import { createServer, request as httpRequest } from "node:http";
import { connect } from "node:net";
import { readFile, stat } from "node:fs/promises";
import { extname, join, resolve, sep } from "node:path";

/**
 * Development-only stand-in for how the hub serves the web app, for the desktop smoke: the built web client's files,
 * `index.html` for every page route, and `/api/` (HTTP and WebSocket) forwarded to the synthetic hub.
 *
 * With a prefix it behaves like a reverse proxy that mounts the hub below the origin root: only paths inside the
 * prefix exist, the prefix is stripped before forwarding, and `index.html` gets `<base href="/<prefix>/">` as the hub
 * writes it for `X-Forwarded-Prefix` (docs/web/web.md "Subpaths"). Every request outside the prefix is answered 404
 * and the page's own are listed, so a page that builds a root path instead of a prefixed one fails the smoke rather
 * than passing by luck.
 */
const TYPES = {
  ".html": "text/html; charset=utf-8", ".js": "text/javascript", ".css": "text/css", ".json": "application/json",
  ".svg": "image/svg+xml", ".png": "image/png", ".ico": "image/x-icon", ".woff2": "font/woff2", ".wasm": "application/wasm",
  ".webmanifest": "application/manifest+json", ".txt": "text/plain; charset=utf-8", ".br": "application/octet-stream",
};

export async function startGateway({ dist, mockPort, prefix = "" }) {
  if (prefix && !/^\/[A-Za-z0-9._~-]+$/.test(prefix)) throw new Error("The smoke prefix must be one clean path segment");
  const root = resolve(dist);
  const index = (await readFile(join(root, "index.html"), "utf8"));
  if (!index.includes('<base href="/" />')) throw new Error("web/dist/index.html has no <base href=\"/\" /> to rewrite");
  const page = Buffer.from(prefix ? index.replace('<base href="/" />', `<base href="${prefix}/" />`) : index, "utf8");
  const outside = [];
  let strangers = 0;
  /** A request outside the prefix. Chromium marks what a page asks for (fetch metadata, a socket's Origin); a request
   *  with neither is another local program trying the port, not the app, and is only counted. */
  const escaped = (req, method) => {
    if (!req.headers["sec-fetch-mode"] && !req.headers.origin) { strangers += 1; return; }
    outside.push(`${method} ${(req.url ?? "").split("?")[0]} (${req.headers["sec-fetch-dest"] ?? "socket"})`);
  };
  const sockets = new Set();

  /** The path inside the hub, or undefined for a request the proxy would never forward. */
  const inner = url => {
    if (!prefix) return url;
    if (url === prefix || url.startsWith(`${prefix}?`)) return `/${url.slice(prefix.length)}`;
    return url.startsWith(`${prefix}/`) ? url.slice(prefix.length) : undefined;
  };

  const server = createServer(async (req, res) => {
    const path = inner(req.url ?? "/");
    if (path === undefined) {
      escaped(req, req.method);
      res.writeHead(404, { "Content-Type": "text/plain" }).end("outside the hub prefix");
      return;
    }
    if (path.startsWith("/api/")) {
      const upstream = httpRequest({ host: "127.0.0.1", port: mockPort, path, method: req.method, headers: { ...req.headers, host: `127.0.0.1:${mockPort}` } }, answer => {
        res.writeHead(answer.statusCode ?? 502, answer.headers);
        answer.pipe(res);
      });
      upstream.on("error", () => { if (!res.headersSent) res.writeHead(502); res.end(); });
      req.pipe(upstream);
      return;
    }
    const pathname = decodeURIComponent(path.split("?")[0]);
    const file = resolve(join(root, pathname));
    let body = page, type = TYPES[".html"];
    if (file.startsWith(root + sep) && extname(file) && (await stat(file).catch(() => undefined))?.isFile() && file !== join(root, "index.html")) {
      body = await readFile(file);
      type = TYPES[extname(file)] ?? "application/octet-stream";
    } else if (extname(pathname) && pathname !== "/index.html") {
      res.writeHead(404, { "Content-Type": "text/plain" }).end("not found");
      return;
    }
    res.writeHead(200, { "Content-Type": type, "Content-Length": body.length, "Cache-Control": "no-cache", "X-Content-Type-Options": "nosniff" }).end(body);
  });

  server.on("upgrade", (req, socket, head) => {
    const path = inner(req.url ?? "/");
    if (path === undefined || !path.startsWith("/api/")) {
      if (path === undefined) escaped(req, "UPGRADE");
      socket.end("HTTP/1.1 404 Not Found\r\n\r\n");
      return;
    }
    const upstream = connect(mockPort, "127.0.0.1", () => {
      const headers = [];
      for (let i = 0; i < req.rawHeaders.length; i += 2) {
        headers.push(`${req.rawHeaders[i]}: ${req.rawHeaders[i].toLowerCase() === "host" ? `127.0.0.1:${mockPort}` : req.rawHeaders[i + 1]}`);
      }
      upstream.write(`GET ${path} HTTP/1.1\r\n${headers.join("\r\n")}\r\n\r\n`);
      if (head.length) upstream.write(head);
      upstream.pipe(socket);
      socket.pipe(upstream);
    });
    for (const end of [socket, upstream]) {
      sockets.add(end);
      end.on("error", () => { socket.destroy(); upstream.destroy(); });
      end.on("close", () => { sockets.delete(end); socket.destroy(); upstream.destroy(); });
    }
  });

  await new Promise((done, fail) => { server.once("error", fail); server.listen(0, "127.0.0.1", done); });
  const port = server.address().port;
  return {
    port,
    url: `http://127.0.0.1:${port}${prefix}/`,
    /** The page's requests that escaped the prefix, as "METHOD path (destination)" without their query (it can carry a credential). */
    outside: () => [...outside],
    strangers: () => strangers,
    async close() {
      for (const socket of sockets) socket.destroy();
      server.closeAllConnections();
      await new Promise(done => server.close(done));
    },
  };
}
