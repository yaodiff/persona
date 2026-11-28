import type { HubAccess } from '../core/HubAccess';
import { SocketReceiveBuffer } from '../core/SocketReceiveBuffer';
import { defaultHealthIntakeRoute, legacyClientHeader, legacyOperationHeader } from '../core/WireCompatibility';
import { clientLocale } from '../core/ClientLocale';
import { bundleManager } from '@kit.AbilityKit';
import { deviceInfo } from '@kit.BasicServicesKit';
import { i18n } from '@kit.LocalizationKit';
import { hilog } from '@kit.PerformanceAnalysisKit';
import { AGENT_MESSAGE_HOLD, isAgentMessagePost, heldReceipt202, publicMutationError, writeResultUnknown } from '../core/TransportPolicy';
import { cryptoFramework } from '@kit.CryptoArchitectureKit';
import { cert } from '@kit.DeviceCertificateKit';
import { connection, socket } from '@kit.NetworkKit';
import { rcp } from '@kit.RemoteCommunicationKit';
import { url, util } from '@kit.ArkTS';

const MAX_HTTP_BYTES: number = 4 * 1024 * 1024;
const MAX_BINARY_BYTES: number = 32 * 1024 * 1024;
// Port relays and outgoing messages stay bounded separately from the Hub's fleet snapshots.
const MAX_FRAME_BYTES: number = 1024 * 1024;
const MAX_HUB_EVENT_BYTES: number = 64 * 1024 * 1024;
const MAX_HEADER_BYTES: number = 16 * 1024;
const REQUEST_TIMEOUT_MS: number = 15000;
const NEXT_ADDRESS_TIMEOUT_MS: number = 2500;
const WS_GUID: string = '258EAFA5-E914-47DA-95CA-C5AB0DC85B11';

export interface StreamNetworkRule {
  readonly directOnly:boolean;
  current():boolean;
  bind(tcp:socket.TCPSocket):Promise<void>;
}

export interface TransportProxy {
  readonly host: string;
  readonly port: number;
  readonly username: string;
  readonly password: string;
  isCurrent(): boolean;
}

export class EndpointConfig {
  readonly baseUrl: string;
  readonly hubId: string;
  readonly token: string;
  readonly pin: string;
  readonly managerId: string;
  readonly alternateUrls: string[];
  readonly proxy: TransportProxy | undefined;
  readonly access: HubAccess | undefined;
  constructor(baseUrl: string, hubId: string, token: string, pin: string, managerId: string = '', alternateUrls: string[] = [], proxy?: TransportProxy, access?: HubAccess) {
    this.baseUrl = normalizeBaseUrl(baseUrl);
    this.hubId = hubId;
    this.token = token;
    this.pin = normalizePin(pin);
    this.managerId = managerId;
    this.proxy = proxy; this.access = access;
    if (alternateUrls.length > 8) throw new Error('invalid_endpoint');
    this.alternateUrls = Array.from(new Set<string>(alternateUrls.map((value: string) => { const normalized = normalizeBaseUrl(value); if (!normalized.startsWith('https://')) throw new Error('invalid_endpoint'); return normalized; }))).filter((value: string) => value !== this.baseUrl);
  }
}

export class HttpFailure extends Error {
  readonly status: number;
  readonly code: string;
  readonly resultUnknown: boolean;
  readonly resultPending: boolean;
  readonly userMessage: string;
  constructor(status: number, code: string, resultUnknown: boolean = false, resultPending: boolean = false, userMessage: string = '') {
    super(code || 'request_failed');
    this.name = 'HttpFailure';
    this.status = status;
    this.code = code || 'request_failed';
    this.resultUnknown = resultUnknown;
    this.resultPending = resultPending;
    this.userMessage = userMessage;
  }
}

interface ClientOSMetadata { name: string; version: string; sdk: number; }
interface ClientMetadata { platform: string; version: string; build: string; os: ClientOSMetadata; }

interface ParsedEndpoint {
  origin: string;
  basePath: string;
  hostname: string;
  port: number;
  scheme: string;
  hostHeader: string;
}

interface DirectSocket {
  tcp: socket.TCPSocket;
  family: number;
}

function numericFamily(address: string): number {
  if (address.includes(':')) return 2;
  return /^\d{1,3}(\.\d{1,3}){3}$/.test(address) ? 1 : 0;
}

export interface BytesResponse {
  data: ArrayBuffer;
  contentType: string;
}

function normalizePin(pin: string): string {
  if (!pin) return '';
  const normalized = pin.trim();
  if (!/^[A-Za-z0-9_-]{43}$/.test(normalized)) throw new Error('invalid_pin');
  return normalized;
}

export function normalizeBaseUrl(input: string): string {
  if (!input || input.length > 2048 || /[\r\n\\]/.test(input)) throw new Error('invalid_endpoint');
  let parsed: url.URL;
  try { parsed = url.URL.parseURL(input.trim()); } catch (_) { throw new Error('invalid_endpoint'); }
  const scheme = parsed.protocol.toLowerCase();
  if (scheme !== 'http:' && scheme !== 'https:') throw new Error('invalid_endpoint');
  if (!parsed.hostname || parsed.username || parsed.password || parsed.search || parsed.hash) throw new Error('invalid_endpoint');
  for (const part of parsed.pathname.split('/')) {
    let decoded: string;
    try { decoded = decodeURIComponent(part); } catch (_) { throw new Error('invalid_endpoint'); }
    if (decoded === '.' || decoded === '..' || decoded.includes('/') || decoded.includes('\\')) throw new Error('invalid_endpoint');
  }
  const path = parsed.pathname.replace(/\/+$/, '');
  const normalizedPath = path === '' || path === '/' ? '' : path;
  return parsed.origin + normalizedPath + '/';
}

function endpointParts(baseUrl: string): ParsedEndpoint {
  const parsed = url.URL.parseURL(baseUrl);
  const scheme = parsed.protocol.slice(0, -1).toLowerCase();
  const explicitPort = parsed.port ? Number(parsed.port) : (scheme === 'https' ? 443 : 80);
  if (!Number.isInteger(explicitPort) || explicitPort < 1 || explicitPort > 65535) throw new Error('invalid_endpoint');
  return {
    origin: parsed.origin,
    basePath: parsed.pathname.endsWith('/') ? parsed.pathname : parsed.pathname + '/',
    hostname: parsed.hostname.replace(/^\[|\]$/g, ''),
    port: explicitPort,
    scheme: scheme,
    hostHeader: parsed.host
  };
}

export function hubCredentialOrigin(baseUrl: string): string {
  const endpoint = endpointParts(normalizeBaseUrl(baseUrl));
  const host = endpoint.hostname.toLowerCase();
  return endpoint.scheme + '://' + (host.includes(':') ? '[' + host + ']' : host) + ':' + endpoint.port;
}

function routePath(base: ParsedEndpoint, path: string): string {
  if (!path || path.length > 8192 || !path.startsWith('/') || path.startsWith('//') || /[\\\r\n#\u0000-\u001f]/.test(path))
    throw new Error('invalid_path');
  const queryAt = path.indexOf('?');
  const pathname = queryAt >= 0 ? path.slice(0, queryAt) : path;
  const query = queryAt >= 0 ? path.slice(queryAt) : '';
  if (!pathname.startsWith('/api/v1/') && pathname !== '/api/v1/ws') throw new Error('invalid_path');
  const segments = pathname.split('/');
  // These IDs stay escaped within their individual route segments on the wire.
  const agentMessageRoute = /^\/api\/v1\/sessions\/[^/]+\/[^/]+\/(?:messages|held\/[^/]+(?:\/retry)?)$/.test(pathname);
  for (let i = 0; i < segments.length; i++) {
    let decoded: string;
    try { decoded = decodeURIComponent(segments[i]); } catch (_) { throw new Error('invalid_path'); }
    const opaqueId = agentMessageRoute && (i === 4 || i === 5 || i === 7);
    if (decoded.includes('\\') || decoded.split('/').some((part: string) => part === '.' || part === '..') ||
      (decoded.includes('/') && (!opaqueId || decoded.split('/').includes('')))) throw new Error('invalid_path');
  }
  const joined = base.basePath + pathname.slice(1);
  let target: url.URL;
  try { target = url.URL.parseURL(base.origin + joined + query); } catch (_) { throw new Error('invalid_path'); }
  if (target.origin !== base.origin || !target.pathname.startsWith(base.basePath)) throw new Error('invalid_path');
  return target.pathname + target.search;
}

function utf8(value: string): Uint8Array {
  if (!value) return new Uint8Array(0);
  return new util.TextEncoder().encodeInto(value);
}

function utf8String(value: Uint8Array, fatal: boolean = true): string {
  if (value.length === 0) return '';
  return util.TextDecoder.create('utf-8', { fatal: fatal }).decodeToString(value);
}

function arrayBufferBytes(value: ArrayBuffer): Uint8Array { return new Uint8Array(value); }

function toArrayBuffer(value: Uint8Array): ArrayBuffer {
  const copy = new Uint8Array(value.length);
  copy.set(value);
  return copy.buffer;
}

function readU16(bytes: Uint8Array, offset: number): number { return (bytes[offset] << 8) | bytes[offset + 1]; }

function concatArrays(parts: Uint8Array[]): Uint8Array {
  let size = 0;
  for (const part of parts) size += part.length;
  const result = new Uint8Array(size);
  let offset = 0;
  for (const part of parts) { result.set(part, offset); offset += part.length; }
  return result;
}

function sha256SPKI(publicKey: cryptoFramework.PubKey): string {
  // X509Cert.getPublicKey().getEncoded() supplies SubjectPublicKeyInfo; its format label is not present on every runtime.
  const spki = publicKey.getEncoded().data;
  if (!spki || spki.length < 32 || spki[0] !== 0x30) throw new Error('pin_spki_encoding');
  const md = cryptoFramework.createMd('SHA256');
  md.updateSync({ data: spki });
  const digest = md.digestSync();
  return new util.Base64Helper().encodeToStringSync(digest.data, util.Type.BASIC_URL_SAFE).replace(/=+$/, '');
}

async function certificateMatchesPin(encoded: cert.EncodingBlob, pin: string): Promise<boolean> {
  if ((encoded.encodingFormat !== cert.EncodingFormat.FORMAT_DER && encoded.encodingFormat !== cert.EncodingFormat.FORMAT_PEM) ||
    encoded.data.length > 128 * 1024) return false;
  const x509 = await cert.createX509Cert(encoded);
  const actual = await sha256SPKI(x509.getPublicKey());
  return constantTimeEqual(actual, pin);
}

function constantTimeEqual(left: string, right: string): boolean {
  let difference = left.length ^ right.length;
  const max = Math.max(left.length, right.length);
  for (let i = 0; i < max; i++) difference |= (left.charCodeAt(i) || 0) ^ (right.charCodeAt(i) || 0);
  return difference === 0;
}

function errorCode(body: string, path: string, method: string, status: number): string {
  if (!body || body.length > 64 * 1024) return 'http_error';
  try {
    const parsed = JSON.parse(body) as Record<string, Object>;
    const code = parsed['code'];
    // Older hubs reject dest=attachment before reading any file bytes. Preserve that exact refusal for a temp upload.
    if ((code === undefined || code === 'bad_request') && status === 400 && method === 'POST' && /^\/api\/v1\/machines\/[^/]+\/files\?/.test(path) &&
      /[?&]dest=attachment(?:&|$)/.test(path) && (parsed['error'] === 'unknown dest' || parsed['detail'] === 'unknown dest'))
      return 'unknown_upload_destination';
    // The Hub exposes both a general HTTP code and a precise receipt error. A missing
    // operation can be checked safely only on its authenticated operation-status route.
    if (method === 'GET' && status === 404 && code === 'not_found' && parsed['error'] === 'no such operation' &&
      /^\/api\/v1\/ops\/[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i.test(path)) return 'no_such_operation';
    if (typeof code === 'string' && code.length <= 128) return code;
    const error = parsed['error'];
    if (typeof error === 'string' && error.length <= 128) return error;
  } catch (_) { }
  return 'http_error';
}

export class HubTransport {
  private readonly endpoint: ParsedEndpoint;
  private readonly hubId: string;
  private readonly token: string;
  private readonly pin: string;
  private readonly managerId: string;
  private readonly streamPath: string;
  private readonly proxy: TransportProxy | undefined;
  private children: HubTransport[] = [];
  private generation: number = 1;
  private closed: boolean = false;
  private pinValidation: string = 'not_called';
  private connectionStage: string = 'idle';
  private closing: boolean = false;
  private queuedFrames: number = 0;
  private queuedBytes: number = 0;
  private requests: Set<rcp.Session> = new Set();
  private tlsSocket: socket.TLSSocket | undefined;
  private tcpSocket: socket.TCPSocket | undefined;
  private messageCallback: ((payload: string) => void) | undefined;
  private binaryCallback: ((payload: ArrayBuffer) => void) | undefined;
  private stateCallback: ((state: string, cause?: Error) => void) | undefined;
  private socketMessageListener: ((info: socket.SocketMessageInfo) => void) | undefined;
  private socketErrorListener: ((error: Error) => void) | undefined;
  private socketCloseListener: (() => void) | undefined;
  private rx = new SocketReceiveBuffer();
  private readonly inboundLimit: number;
  private upgraded: boolean = false;
  private socketTrusted: boolean = false;
  private upgradeRequested: boolean = false;
  private openResolve: (() => void) | undefined;
  private openReject: ((error: Error) => void) | undefined;
  private fragmentOpcode: number = 0;
  private fragmentParts: Uint8Array[] = [];
  private fragmentSize: number = 0;
  private sendQueue: Promise<void> = Promise.resolve();

  private readonly access: HubAccess | undefined;
  private readonly credentialOrigin: string;

  constructor(config: EndpointConfig, streamPath: string = '/api/v1/ws') {
    this.endpoint = endpointParts(config.baseUrl);
    this.access = config.access; this.credentialOrigin = hubCredentialOrigin(config.baseUrl);
    this.hubId = config.hubId;
    this.token = config.token;
    this.pin = config.pin;
    this.inboundLimit = streamPath === '/api/v1/ws' ? MAX_HUB_EVENT_BYTES : MAX_FRAME_BYTES;
    this.managerId = config.managerId; this.streamPath = streamPath; routePath(this.endpoint, streamPath);
    this.proxy = config.proxy;
    if (this.proxy && (this.endpoint.scheme !== 'https' || this.proxy.host !== '127.0.0.1' || !this.pin)) throw new Error('ssh_invalid_config');
    if (this.token && this.endpoint.scheme !== 'https') throw new Error('credentials_require_https');
    if (this.pin && this.endpoint.scheme !== 'https') throw new Error('pin_requires_https');
  }

  private streamNetwork:StreamNetworkRule|undefined;
  private outgoingLimit(opcode:number):number {
    if(opcode===1 && this.streamNetwork && /^\/api\/v1\/recordings\/[a-f0-9-]{36}\/audio$/.test(this.streamPath))return 8704*1024;
    return MAX_FRAME_BYTES;
  }
  /** Local equality witness for a recording consent across app restarts. No device credential enters its manifest. */
  meetingCredentialKey():string {
    this.assertLive(this.generation);
    if(!this.token)throw new Error('missing_credential');
    const md=cryptoFramework.createMd('SHA256');
    md.updateSync({data:utf8('persona.meeting.consent.v1\u0000'+this.token)});
    return new util.Base64Helper().encodeToStringSync(md.digestSync().data,util.Type.BASIC_URL_SAFE).replace(/=+$/,'');
  }
  stream(path: string, network?:StreamNetworkRule): HubTransport {
    this.assertLive(this.generation); this.children = this.children.filter((child: HubTransport) => !child.closed);
    if (this.children.length >= 4) throw new Error('stream_limit');
    const config = new EndpointConfig(this.endpoint.origin + this.endpoint.basePath, this.hubId, this.token, this.pin, this.managerId, [], this.proxy, this.access);
    if (network && (!network.current() || this.proxy && network.directOnly)) throw new Error('audio_network_unverified');
    const child = new HubTransport(config, path); child.streamNetwork=network; this.children.push(child); return child;
  }
  private assertLive(generation: number): void {
    if (this.closed || this.closing || generation !== this.generation || this.proxy && !this.proxy.isCurrent()) throw new HttpFailure(0, 'connection_closed');
  }

  private clientMetadata(): ClientMetadata {
    const info = bundleManager.getBundleInfoForSelfSync(bundleManager.BundleFlag.GET_BUNDLE_INFO_DEFAULT);
    const os: ClientOSMetadata = { name: 'HarmonyOS', version: deviceInfo.osFullName, sdk: deviceInfo.sdkApiVersion };
    const version = /^[A-Za-z0-9._+-]{1,64}$/.test(info.versionName) ? info.versionName : String(info.versionCode);
    return { platform: 'harmony', version: version, build: String(info.versionCode), os: os };
  }
  private clientHeaders(): Record<string, string> {
    const metadata = this.clientMetadata(), identity = metadata.platform + '/' + metadata.version;
    const headers: Record<string, string> = {
      'X-Persona-Client': identity,
      'X-Persona-Source': 'app',
      'Accept-Language': clientLocale(i18n.getSystemLanguage()),
      'X-Persona-Elicitation': '1'
    };
    headers[legacyClientHeader] = identity; return headers;
  }

  hello(features: string[]): boolean {
    const identity = JSON.parse(JSON.stringify(this.clientMetadata())) as Record<string, Object>;
    identity['type'] = 'client_hello'; identity['features'] = features.concat([AGENT_MESSAGE_HOLD]); identity['locale'] = clientLocale(i18n.getSystemLanguage());
    return this.send(JSON.stringify(identity));
  }
  private authHeaders(includeManager:boolean = true): Record<string, string> {
    const headers = this.clientHeaders();
    this.access?.apply(this.credentialOrigin, headers);
    if (this.token) headers['Authorization'] = 'Bearer ' + this.token;
    if (includeManager && this.managerId) headers['X-Persona-Manager'] = this.managerId;
    return headers;
  }

  private accessDenial(status: number, header: (name: string) => string, body?: Uint8Array): HttpFailure | undefined {
    let blocked = false;
    const location = header('location');
    if (status >= 300 && status < 400 && location) {
      try {
        const host = url.URL.parseURL(location, this.endpoint.origin).hostname.toLowerCase();
        blocked = host === 'cloudflareaccess.com' || host.endsWith('.cloudflareaccess.com');
      } catch (_) {}
    }
    if (!blocked && header('content-type').toLowerCase().includes('html')) {
      if (body && body.length) {
        const text = utf8String(body.subarray(0, 64 * 1024), false).toLowerCase();
        blocked = text.includes('cloudflareaccess.com') || text.includes('/cdn-cgi/access/');
      } else {
        blocked = (status === 401 || status === 403) && (!!header('cf-ray') || header('server').toLowerCase().includes('cloudflare'));
      }
    }
    if (!blocked) return undefined;
    return new HttpFailure(status, this.access?.origin === this.credentialOrigin ? 'cloudflare_access_rejected' : 'cloudflare_access_required');
  }

  private async validateContextPin(context: rcp.ValidationContext): Promise<boolean> {
    this.pinValidation = 'missing_certificate';
    if (!this.pin) return false;
    try {
      let encoded: cert.EncodingBlob;
      if (context.x509Certs && context.x509Certs.length) encoded = await context.x509Certs[0].getEncoded();
      else if (context.pemCerts && context.pemCerts.length) encoded = { data: utf8(context.pemCerts[0]), encodingFormat: cert.EncodingFormat.FORMAT_PEM };
      else return false;
      const valid = await certificateMatchesPin(encoded, this.pin); this.pinValidation = valid ? 'matched' : 'pin_mismatch'; return valid;
    } catch (error) {
      const fields = error as Object as Record<string, Object>, code = fields ? fields['code'] : undefined;
      this.pinValidation = error instanceof Error && /^[a-z_0-9]{1,64}$/.test(error.message) ? error.message : typeof code === 'number' ? String(code) : 'validation_unavailable';
      return false;
    }
  }

  private async requestData(path: string, method: string, body: rcp.RequestContent | undefined, contentType: string, limit: number, opId: string, acceptTransfer: boolean = false): Promise<BytesResponse> {
    const generation = this.generation; this.assertLive(generation);
    const verb = method.toUpperCase();
    if (!['GET', 'POST', 'PUT', 'PATCH', 'DELETE', 'HEAD'].includes(verb)) throw new Error('invalid_method');
    const target = this.endpoint.origin + routePath(this.endpoint, path), headers = this.authHeaders(!defaultHealthIntakeRoute(path,verb));
    const messagePost = isAgentMessagePost(verb, path);
    if (messagePost) headers['X-Persona-Features'] = AGENT_MESSAGE_HOLD;
    if (body !== undefined) headers['Content-Type'] = contentType;
    if (opId) {
      if (!/^[A-Za-z0-9._:-]{1,128}$/.test(opId)) throw new Error('invalid_operation_id');
      headers['X-Persona-Op-Id'] = opId; headers[legacyOperationHeader] = opId;
    }
    const session = rcp.createSession(); this.requests.add(session);
    const request = new rcp.Request(target, verb as rcp.HttpMethod, headers, body);
    // Git pull allows the Hub 90 seconds, and checkout creation/removal may wait longer on its host.
    const worktreeRequest = verb === 'POST' && /^\/api\/v1\/machines\/[^/]+\/worktree\/(create|delete|add|remove)$/.test(path);
    const pullRequest = verb === 'POST' && /^\/api\/v1\/machines\/[^/]+\/git\/pull$/.test(path);
    const responseTimeout = worktreeRequest ? 165000 : pullRequest ? 100000 : REQUEST_TIMEOUT_MS;
    request.configuration = { transfer: { autoRedirect: false, timeout: { connectMs: REQUEST_TIMEOUT_MS, transferMs: responseTimeout, inactivityMs: responseTimeout } },
      security: { remoteValidation: this.pin ? (context: rcp.ValidationContext) => this.validateContextPin(context) : 'system' } };
    if (this.proxy) request.configuration.proxy = { url: 'http://' + this.proxy.host + ':' + this.proxy.port,
      createTunnel: 'always', security: { serverAuthentication: { authenticationType: 'basic', credential: {
        username: this.proxy.username, password: this.proxy.password } } } };
    request.cacheControl = { noCache: true, noStore: true };
    const chunks: Uint8Array[] = []; let received = 0, overLimit = false, dispatched = false;
    request.destination = (chunk: ArrayBuffer): void => {
      if (received + chunk.byteLength > limit) { overLimit = true; session.cancel(request); return; }
      received += chunk.byteLength; chunks.push(new Uint8Array(chunk).slice());
    };
    try {
      if(this.streamNetwork && (!this.streamNetwork.current() || this.streamNetwork.directOnly))throw new HttpFailure(0,'audio_network_unverified');
      this.assertLive(generation); dispatched = true; const response = await session.fetch(request); this.assertLive(generation);
      if (overLimit) throw new HttpFailure(0, 'response_too_large');
      const bytes = concatArrays(chunks);
      const header = (name: string): string => {
        const key = Object.keys(response.headers).find((k: string) => k.toLowerCase() === name);
        const value = key ? response.headers[key] : undefined;
        return typeof value === 'string' ? value : Array.isArray(value) ? value.join(', ') : '';
      };
      const denied = this.accessDenial(response.statusCode, header, bytes);
      if (denied) throw new HttpFailure(denied.status, denied.code, !['GET','HEAD'].includes(verb) && denied.status >= 200 && denied.status < 300);
      if (response.statusCode >= 300 && response.statusCode < 400) throw new HttpFailure(response.statusCode, 'redirect_refused');
      const text = response.statusCode < 200 || response.statusCode >= 300 ? utf8String(bytes, false) : '';
      if (response.statusCode === 202 && !['GET', 'HEAD'].includes(verb) && !acceptTransfer) {
        const state = messagePost ? heldReceipt202(utf8String(bytes, false), opId) : 'pending';
        if (state !== 'held') throw new HttpFailure(202, state === 'unknown' ? 'result_unknown' : 'pending', true, state === 'pending');
      }
      if (response.statusCode < 200 || response.statusCode >= 300) {
        const userMessage=publicMutationError(verb,path,response.statusCode,text);
        throw new HttpFailure(response.statusCode,errorCode(text,path,verb,response.statusCode),
          !['GET','HEAD'].includes(verb) && writeResultUnknown(response.statusCode,text,pullRequest),false,userMessage);
      }
      const responseType = response.headers['content-type'] || response.headers['Content-Type'];
      return { data: toArrayBuffer(bytes), contentType: typeof responseType === 'string' ? responseType : '' };
    } catch (error) {
      if (error instanceof HttpFailure) throw new HttpFailure(error.status, error.code, error.resultUnknown || (dispatched && !['GET', 'HEAD'].includes(verb) && (error.status === 0 || error.status >= 300 && error.status < 400)), error.resultPending, error.userMessage);
      const fields = error as Object as Record<string, Object>, code = fields ? fields['code'] : undefined;
      const diagnostic = overLimit ? 'response_too_large' : (code === 2300060 || code === 1007900060) && this.pin ? 'tls_' + this.pinValidation : typeof code === 'number' ? 'transport_failed_' + code : 'transport_failed';
      throw new HttpFailure(0, diagnostic, dispatched && !['GET', 'HEAD'].includes(verb));
    } finally { this.requests.delete(session); try { session.close(); } catch (_) {} }
  }

  async request(path: string, method: string = 'GET', body: string = '', opId: string = '', messageRequestBytes: number = MAX_HTTP_BYTES): Promise<string> {
    const verb = method.toUpperCase();
    const requestLimit = isAgentMessagePost(verb, path) ? messageRequestBytes : MAX_HTTP_BYTES;
    if (utf8(body).length > requestLimit) throw new HttpFailure(0, 'request_too_large');
    const responseLimit = verb === 'GET' && path.split('?')[0] === '/api/v1/sessions' ? MAX_HUB_EVENT_BYTES : MAX_HTTP_BYTES;
    const response = await this.requestData(path, method, body || undefined, 'application/json', responseLimit, opId);
    try { return utf8String(new Uint8Array(response.data), true); }
    catch (_) { throw new HttpFailure(0, 'response_invalid', !['GET', 'HEAD'].includes(verb)); }
  }
  /** Only the transfer protocol defines 202 as a task snapshot; it is still not completion. */
  async requestAccepted(path: string, method: string, body: string): Promise<string> {
    if (path.split('?')[0] !== '/api/v1/files/transfer' || method !== 'POST') throw new Error('invalid_path');
    if (utf8(body).length > MAX_HTTP_BYTES) throw new HttpFailure(0, 'request_too_large');
    const response = await this.requestData(path, method, body || undefined, 'application/json', MAX_HTTP_BYTES, '', true);
    try { return utf8String(new Uint8Array(response.data), true); }
    catch (_) { throw new HttpFailure(0, 'response_invalid', true); }
  }
  async requestBytes(path: string, method: string = 'GET', body?: ArrayBuffer, contentType: string = 'application/octet-stream', opId: string = '', requestLimit: number = MAX_BINARY_BYTES): Promise<BytesResponse> {
    if (body && body.byteLength > requestLimit) throw new HttpFailure(0, 'request_too_large');
    return this.requestData(path, method, body, contentType, MAX_BINARY_BYTES, opId);
  }
  async putMeetingGap(path:string,data:ArrayBuffer):Promise<string>{
    if(!this.streamNetwork || !/^\/api\/v1\/recordings\/[0-9a-f-]{36}\/audio\?at=\d+(?:\.\d+)?$/.test(path) || !data.byteLength || data.byteLength%2 || data.byteLength>64000)
      throw new Error('invalid_audio_gap');
    const result=await this.requestData(path,'PUT',data,'application/octet-stream',MAX_HTTP_BYTES,'');
    return utf8String(new Uint8Array(result.data),true);
  }

  private async withTimeout<T>(promise: Promise<T>, timeout: number, code: string): Promise<T> {
    let timer: number = -1;
    try {
      return await Promise.race([promise, new Promise<T>((_, reject) => {
        timer = setTimeout(() => reject(new Error(code)), timeout);
      })]);
    } finally { if (timer !== -1) clearTimeout(timer); }
  }

  async connect(onMessage: (payload: string) => void, onState?: (state: string, cause?: Error) => void,
    onBinary?: (payload: ArrayBuffer) => void): Promise<void> {
    this.assertLive(this.generation);
    if (this.connectionStage !== 'idle') throw new Error('already_connecting');
    if (!this.token) throw new Error('missing_credential');
    this.messageCallback = onMessage;
    this.binaryCallback = onBinary;
    this.stateCallback = onState;
    const generation = this.generation;
    this.connectionStage = 'open_socket';
    try {
      const requestPath = routePath(this.endpoint, this.streamPath);
      const cancelled = new Promise<void>((_, reject) => { this.openReject = reject; });
      const open = this.endpoint.scheme === 'https' ? this.connectSecure(requestPath, generation) : this.connectPlain(requestPath, generation);
      await Promise.race([open, cancelled]);
      this.assertLive(generation);
      this.stateCallback?.('ready');
    } catch (error) {
      const fields = error as Object as Record<string, Object>; const code = fields ? fields['code'] : undefined;
      this.failSocket(error instanceof Error ? error : new Error(typeof code === 'number' ? 'native_' + code : 'connect_failed'));
      throw error instanceof Error ? error : new Error('connect_failed');
    } finally { this.openReject = undefined; }
  }

  private attachSocketHandlers(
    onMessage: (info: socket.SocketMessageInfo) => void,
    onError: (error: Error) => void,
    onClose: () => void
  ): void {
    this.socketMessageListener = onMessage;
    this.socketErrorListener = onError;
    this.socketCloseListener = onClose;
    if (this.tlsSocket) {
      this.tlsSocket.on('message', onMessage);
      this.tlsSocket.on('error', onError);
      this.tlsSocket.on('close', onClose);
    } else if (this.tcpSocket) {
      this.tcpSocket.on('message', onMessage);
      this.tcpSocket.on('error', onError);
      this.tcpSocket.on('close', onClose);
    }
  }

  private async directPeers(): Promise<socket.NetAddress[]> {
    const host = this.endpoint.hostname, port = this.endpoint.port, family = numericFamily(host);
    if (family) return [{ address: host, port: port, family: family }];
    this.connectionStage = 'resolve_address';
    const answers = await this.withTimeout(connection.getAddressesByName(host), REQUEST_TIMEOUT_MS, 'dns_timeout');
    const ipv4: socket.NetAddress[] = [], ipv6: socket.NetAddress[] = [], seen = new Set<string>();
    for (const answer of answers) {
      const kind = numericFamily(answer.address);
      if (!kind || seen.has(answer.address)) continue;
      seen.add(answer.address);
      (kind === 2 ? ipv6 : ipv4).push({ address: answer.address, port: port, family: kind });
    }
    const peers: socket.NetAddress[] = [];
    for (let index = 0; index < Math.max(ipv4.length, ipv6.length); index++) {
      if (index < ipv6.length) peers.push(ipv6[index]);
      if (index < ipv4.length) peers.push(ipv4[index]);
    }
    if (!peers.length) throw new Error('dns_no_addresses');
    return peers;
  }

  private async openDirectSocket(generation: number, deadline: number): Promise<DirectSocket> {
    const peers = await this.directPeers();
    this.assertLive(generation);
    this.connectionStage = 'connect_tcp';
    for (let index = 0; index < peers.length; index++) {
      const remaining = deadline - Date.now();
      if (remaining <= 0) throw new Error('connect_timeout');
      const count = peers.length - index;
      const timeout = count === 1 ? remaining : Math.min(NEXT_ADDRESS_TIMEOUT_MS, Math.ceil(remaining / count));
      const peer = peers[index], tcp = socket.constructTCPSocketInstance();
      this.tcpSocket = tcp;
      try {
        if(this.streamNetwork?.directOnly)await tcp.bind({address:peer.family===2 ? '::':'0.0.0.0',port:0,family:peer.family});
        if (this.streamNetwork) await this.streamNetwork.bind(tcp);
        await this.withTimeout(tcp.connect({ address: peer, timeout: timeout }), timeout, 'connect_timeout');
        this.assertLive(generation);
        return { tcp: tcp, family: peer.family || 1 };
      } catch (error) {
        if (this.tcpSocket === tcp) {
          this.tcpSocket = undefined;
          tcp.close().catch((): void => {});
        }
        this.assertLive(generation);
        if (index === peers.length - 1) throw error as Error;
      }
    }
    throw new Error('connect_timeout');
  }

  private async connectSecure(requestPath: string, generation: number): Promise<void> {
    const deadline = Date.now() + REQUEST_TIMEOUT_MS;
    let ws: socket.TLSSocket, family: number;
    if (this.proxy) {
      family = numericFamily(this.endpoint.hostname) === 2 ? 2 : 1;
      ws = socket.constructTLSSocketInstance();
      this.tlsSocket = ws;
      this.connectionStage = 'bind_tls';
      await ws.bind({ address: family === 2 ? '::' : '0.0.0.0', port: 0, family: family });
    } else {
      const direct = await this.openDirectSocket(generation, deadline);
      this.assertLive(generation);
      // NetworkKit transfers the connected descriptor to TLS. Only TCP failures try another address.
      ws = socket.constructTLSSocketInstance(direct.tcp);
      this.tlsSocket = ws;
      this.tcpSocket = undefined;
      family = direct.family;
    }
    this.assertLive(generation);
    this.attachSocketHandlers((info: socket.SocketMessageInfo) => this.receive(info.message),
      (error: Error) => this.failSocket(error), () => this.failSocket(new Error('socket_closed')));
    this.connectionStage = 'connect_tls';
    const timeout = this.proxy ? REQUEST_TIMEOUT_MS : deadline - Date.now();
    if (timeout <= 0) throw new Error('connect_timeout');
    const options: socket.TLSConnectOptions = {
      address: { address: this.endpoint.hostname, port: this.endpoint.port, family: family },
      secureOptions: { protocols: [socket.Protocol.TLSv13, socket.Protocol.TLSv12] },
      ALPNProtocols: ['http/1.1'],
      skipRemoteValidation: !!this.pin,
      timeout: timeout
    };
    if (this.proxy) options.proxy = { type: socket.ProxyTypes.SOCKS5, address: { address: this.proxy.host, port: this.proxy.port, family: 1 },
      username: this.proxy.username, password: this.proxy.password };
    this.assertLive(generation);
    await this.withTimeout(ws.connect(options), timeout, 'connect_timeout');
    this.assertLive(generation);
    if (this.pin) {
      this.connectionStage = 'cert_pin';
      const remote = await this.withTimeout(ws.getRemoteCertificate(), REQUEST_TIMEOUT_MS, 'certificate_timeout');
      if (!await certificateMatchesPin(remote, this.pin)) throw new Error('hub_pin_mismatch');
    }
    this.assertLive(generation); this.socketTrusted = true;
    this.connectionStage = 'upgrade_key';
    await this.sendUpgrade(requestPath);
    this.connectionStage = 'upgrade_response';
    await this.waitForUpgrade(generation);
  }

  private async connectPlain(requestPath: string, generation: number): Promise<void> {
    await this.openDirectSocket(generation, Date.now() + REQUEST_TIMEOUT_MS);
    this.assertLive(generation);
    this.attachSocketHandlers((info: socket.SocketMessageInfo) => this.receive(info.message),
      (error: Error) => this.failSocket(error), () => this.failSocket(new Error('socket_closed')));
    this.assertLive(generation); this.socketTrusted = true;
    this.connectionStage = 'upgrade_key';
    await this.sendUpgrade(requestPath);
    this.connectionStage = 'upgrade_response';
    await this.waitForUpgrade(generation);
  }

  private async sendUpgrade(requestPath: string): Promise<void> {
    const random = await cryptoFramework.createRandom().generateRandom(16);
    const key = new util.Base64Helper().encodeToStringSync(random.data);
    const headers = this.authHeaders();
    const lines: string[] = [
      'GET ' + requestPath + ' HTTP/1.1',
      'Host: ' + this.endpoint.hostHeader,
      'Upgrade: websocket',
      'Connection: Upgrade',
      'Sec-WebSocket-Key: ' + key,
      'Sec-WebSocket-Version: 13'
    ];
    for (const name of Object.keys(headers)) lines.push(name + ': ' + headers[name]);
    const request = lines.join('\r\n') + '\r\n\r\n';
    this.expectedAccept = await this.websocketAccept(key);
    if (!this.socketTrusted || this.closed || this.closing) throw new Error('untrusted_socket');
    this.upgradeRequested = true;
    this.connectionStage = 'upgrade_write';
    await this.withTimeout(this.socketSend(request), REQUEST_TIMEOUT_MS, 'upgrade_write_timeout');
  }

  private expectedAccept: string = '';
  private async websocketAccept(key: string): Promise<string> {
    const md = cryptoFramework.createMd('SHA1');
    await md.update({ data: utf8(key + WS_GUID) });
    const digest = await md.digest();
    return new util.Base64Helper().encodeToStringSync(digest.data);
  }

  private waitForUpgrade(generation: number): Promise<void> {
    return this.withTimeout(new Promise<void>((resolve, reject) => {
      this.openResolve = resolve;
      this.openReject = reject;
      if (this.upgraded) resolve();
      if (this.closed || this.closing || generation !== this.generation) reject(new Error('connection_closed'));
    }), REQUEST_TIMEOUT_MS, 'upgrade_timeout').finally(() => {
      this.openResolve = undefined;
      this.openReject = undefined;
    });
  }

  private receive(buffer: ArrayBuffer): void {
    if (this.closed || this.closing) return;
    if (!this.socketTrusted || !this.upgradeRequested || this.expectedAccept.length !== 28) { this.failSocket(new Error('unsolicited_data')); return; }
    const bytes = arrayBufferBytes(buffer);
    let pos = 0;
    while (pos < bytes.length && !this.closed && !this.closing) {
      const limit = this.upgraded ? this.inboundLimit + 10 : MAX_HEADER_BYTES + 4;
      const take = Math.min(bytes.length - pos, limit - this.rx.length);
      if (take <= 0) { this.failSocket(new Error(this.upgraded ? 'frame_too_large' : 'invalid_upgrade')); return; }
      this.rx.append(bytes.subarray(pos, pos + take), limit);
      pos += take;
      if (!this.upgraded) this.processUpgradeResponse();
      if (this.upgraded) this.processFrames();
    }
  }

  private processUpgradeResponse(): void {
    const received = this.rx.view();
    const end = this.headerEnd(received);
    if (end < 0) {
      if (received.length === MAX_HEADER_BYTES + 4) this.failSocket(new Error('invalid_upgrade'));
      return;
    }
    if (end > MAX_HEADER_BYTES) { this.failSocket(new Error('invalid_upgrade')); return; }
    let header: string;
    try { header = utf8String(received.subarray(0, end), true); } catch (_) { this.failSocket(new Error('invalid_upgrade')); return; }
    const lines = header.split('\r\n');
    const parsed: Map<string, string> = new Map();
    for (const line of lines.slice(1)) {
      const colon = line.indexOf(':');
      if (colon <= 0) continue;
      const name = line.slice(0, colon).trim().toLowerCase();
      const value = line.slice(colon + 1).trim();
      parsed.set(name, (parsed.get(name) || '') + (parsed.has(name) ? ', ' : '') + value);
    }
    if (!/^HTTP\/1\.[01] 101(?: |$)/.test(lines[0])) {
      const status = Number(lines[0].split(' ')[1] || '0');
      const denied = this.accessDenial(status, (name: string): string => parsed.get(name) || '');
      this.failSocket((denied || new HttpFailure(status, 'upgrade_rejected')) as Error); return;
    }
    const upgrade = (parsed.get('upgrade') || '').toLowerCase().split(',').map((v: string) => v.trim());
    const connection = (parsed.get('connection') || '').toLowerCase().split(',').map((v: string) => v.trim());
    const accept = parsed.get('sec-websocket-accept') || '';
    if (!accept || !this.socketTrusted || !this.upgradeRequested || !upgrade.includes('websocket') || !connection.includes('upgrade') || !constantTimeEqual(accept, this.expectedAccept)) {
      this.failSocket(new Error('invalid_upgrade')); return;
    }
    this.rx.consume(end + 4);
    this.upgraded = true;
    this.openResolve?.();
  }

  private headerEnd(bytes: Uint8Array): number {
    for (let i = 0; i + 3 < bytes.length; i++)
      if (bytes[i] === 13 && bytes[i + 1] === 10 && bytes[i + 2] === 13 && bytes[i + 3] === 10) return i;
    return -1;
  }

  private processFrames(): void {
    while (!this.closed && !this.closing && this.rx.length >= 2) {
      const received = this.rx.view();
      const first = received[0], second = received[1];
      const fin = (first & 0x80) !== 0, rsv = first & 0x70, opcode = first & 0x0f;
      const masked = (second & 0x80) !== 0;
      let length = second & 0x7f, offset = 2;
      if (rsv !== 0 || masked) { this.failSocket(new Error('invalid_frame')); return; }
      if (length === 126) {
        if (received.length < 4) return;
        length = readU16(received, 2); offset = 4;
      } else if (length === 127) {
        if (received.length < 10) return;
        if ((received[2] & 0x80) !== 0) { this.failSocket(new Error('invalid_frame')); return; }
        const high = readU16(received, 2) * 65536 + readU16(received, 4);
        const low = readU16(received, 6) * 65536 + readU16(received, 8);
        if (high !== 0 || low > this.inboundLimit) { this.failSocket(new Error('frame_too_large')); return; }
        length = low; offset = 10;
      }
      if (length > this.inboundLimit) { this.failSocket(new Error('frame_too_large')); return; }
      const control = opcode >= 8;
      if (control && (!fin || length > 125 || (opcode !== 8 && opcode !== 9 && opcode !== 10))) {
        this.failSocket(new Error('invalid_control_frame')); return;
      }
      if (opcode !== 0 && opcode !== 1 && opcode !== 2 && !control) { this.failSocket(new Error('invalid_frame')); return; }
      if (opcode === 0 && this.fragmentSize + length > this.inboundLimit) { this.failSocket(new Error('message_too_large')); return; }
      if (received.length < offset + length) return;
      const payload = received.slice(offset, offset + length);
      this.rx.consume(offset + length);
      if (control) { this.processControl(opcode, payload); continue; }
      if (opcode === 0) {
        if (!this.fragmentOpcode) { this.failSocket(new Error('unexpected_continuation')); return; }
        this.fragmentParts.push(payload); this.fragmentSize += payload.length;
      } else {
        if (this.fragmentOpcode !== 0) { this.failSocket(new Error('interleaved_message')); return; }
        this.fragmentOpcode = opcode;
        this.fragmentParts = [payload]; this.fragmentSize = payload.length;
      }
      if (this.fragmentSize > this.inboundLimit || this.fragmentParts.length > 1024) { this.failSocket(new Error('message_too_large')); return; }
      if (fin) {
        const message = this.fragmentParts.length === 1 ? this.fragmentParts[0] : concatArrays(this.fragmentParts);
        const messageOpcode = this.fragmentOpcode;
        this.fragmentOpcode = 0; this.fragmentParts = []; this.fragmentSize = 0;
        if (messageOpcode === 1) {
          try {
            const text = utf8String(message, true);
            const event = JSON.parse(text) as Record<string, Object>;
            if (this.streamPath === '/api/v1/ws' && event['type'] === 'hello' && this.hubId && event['hub_id'] !== this.hubId) {
              this.failSocket(new Error('hub_identity_mismatch')); return;
            }
            this.messageCallback?.(text);
          }
          catch (_) { this.failSocket(new Error('invalid_utf8')); return; }
        } else if (messageOpcode === 2) {
          this.binaryCallback?.(toArrayBuffer(message));
        }
      }
    }
  }

  private processControl(opcode: number, payload: Uint8Array): void {
    if (opcode === 9) { this.enqueueFrame(10, payload).catch(() => { if (!this.closing) this.failSocket(new Error('socket_write_failed')); }); return; }
    if (opcode === 10) return;
    if (opcode === 8) {
      if (payload.length === 1) { this.failSocket(new Error('invalid_close_frame')); return; }
      if (payload.length >= 2) {
        const code = readU16(payload, 0);
        if (code < 1000 || code === 1004 || code === 1005 || code === 1006 || code === 1015 || (code > 1014 && code < 3000) || code > 4999) {
          this.failSocket(new Error('invalid_close_frame')); return;
        }
        try { utf8String(payload.slice(2), true); } catch (_) { this.failSocket(new Error('invalid_close_frame')); return; }
      }
      this.closing = true;
      this.withTimeout(this.enqueueFrame(8, payload), 500, 'close_timeout').catch(() => undefined)
        .finally(() => this.failSocket(new Error('server_closed')));
    }
  }

  private async socketSend(data: string | ArrayBuffer): Promise<void> {
    this.assertLive(this.generation);
    if (this.tlsSocket) return this.tlsSocket.send(data);
    if (this.tcpSocket) return this.tcpSocket.send({ data: data });
    throw new Error('connection_closed');
  }

  private enqueueFrame(opcode: number, payload: Uint8Array): Promise<void> {
    const limit=this.outgoingLimit(opcode);
    if (payload.length > limit || this.closed || this.closing && opcode !== 8) return Promise.reject(new Error('connection_closed'));
    if (this.queuedFrames >= 64 || this.queuedBytes + payload.length > Math.max(MAX_FRAME_BYTES*2,limit)) { this.failSocket(new Error('send_queue_full')); return Promise.reject(new Error('send_queue_full')); }
    const tls = this.tlsSocket, tcp = this.tcpSocket, copied = payload.slice(), generation = this.generation;
    this.queuedFrames++; this.queuedBytes += copied.length;
    const send = async (): Promise<void> => {
      if (this.closed || this.closing && opcode !== 8 || this.proxy && !this.proxy.isCurrent()) throw new Error('connection_closed');
      const random = await cryptoFramework.createRandom().generateRandom(4);
      if (this.closed || this.streamNetwork && !this.streamNetwork.current() && opcode!==8 || this.closing && opcode !== 8 || generation !== this.generation || tls !== this.tlsSocket || tcp !== this.tcpSocket || this.proxy && !this.proxy.isCurrent()) throw new Error('connection_closed');
      const mask = random.data;
      const maskBit = 0x80;
      let header: Uint8Array;
      if (payload.length < 126) header = new Uint8Array([0x80 | opcode, maskBit | payload.length]);
      else if (payload.length <= 65535) header = new Uint8Array([0x80 | opcode, maskBit | 126, (payload.length >> 8) & 0xff, payload.length & 0xff]);
      else {
        header = new Uint8Array(10); header[0] = 0x80 | opcode; header[1] = maskBit | 127;
        const high = Math.floor(payload.length / 4294967296), low = payload.length >>> 0;
        header[2] = (high >>> 24) & 0xff; header[3] = (high >>> 16) & 0xff; header[4] = (high >>> 8) & 0xff; header[5] = high & 0xff;
        header[6] = (low >>> 24) & 0xff; header[7] = (low >>> 16) & 0xff; header[8] = (low >>> 8) & 0xff; header[9] = low & 0xff;
      }
      const masked = new Uint8Array(payload.length);
      for (let i = 0; i < payload.length; i++) masked[i] = copied[i] ^ mask[i & 3];
      const data = toArrayBuffer(concatArrays([header, mask, masked]));
      if (this.closed || this.streamNetwork && !this.streamNetwork.current() && opcode!==8 || this.closing && opcode !== 8 || generation !== this.generation || tls !== this.tlsSocket || tcp !== this.tcpSocket || this.proxy && !this.proxy.isCurrent()) throw new Error('connection_closed');
      if (tls) await this.withTimeout(tls.send(data), 5000, 'socket_write_timeout');
      else if (tcp) await this.withTimeout(tcp.send({ data: data }), 5000, 'socket_write_timeout');
      else throw new Error('connection_closed');
    };
    const sent = this.sendQueue.then(send).finally(() => { this.queuedFrames--; this.queuedBytes -= copied.length; });
    this.sendQueue = sent.catch(() => {});
    return sent;
  }

  send(payload: string): boolean {
    const bytes = utf8(payload); if (!this.upgraded || this.closed || this.closing || bytes.length > this.outgoingLimit(1) || this.proxy && !this.proxy.isCurrent()) return false;
    this.enqueueFrame(1, bytes).catch(() => { if (!this.closing) this.failSocket(new Error('socket_write_failed')); });
    return !this.closed;
  }

  async sendBinaryAsync(payload:ArrayBuffer):Promise<void> {
    if (!this.upgraded || this.closed || this.closing || payload.byteLength>MAX_FRAME_BYTES ||
      this.streamNetwork && !this.streamNetwork.current() || this.proxy && !this.proxy.isCurrent()) throw new Error('connection_closed');
    await this.enqueueFrame(2,new Uint8Array(payload));
  }

  sendBinary(payload: ArrayBuffer): boolean {
    if (!this.upgraded || this.closed || this.closing || payload.byteLength > MAX_FRAME_BYTES || this.proxy && !this.proxy.isCurrent()) return false;
    this.enqueueFrame(2, new Uint8Array(payload)).catch(() => { if (!this.closing) this.failSocket(new Error('socket_write_failed')); });
    return !this.closed;
  }

  private detachSocketHandlers(): void {
    if (this.tlsSocket) {
      if (this.socketMessageListener) this.tlsSocket.off('message', this.socketMessageListener);
      if (this.socketErrorListener) this.tlsSocket.off('error', this.socketErrorListener);
      if (this.socketCloseListener) this.tlsSocket.off('close', this.socketCloseListener);
    }
    if (this.tcpSocket) {
      if (this.socketMessageListener) this.tcpSocket.off('message', this.socketMessageListener);
      if (this.socketErrorListener) this.tcpSocket.off('error', this.socketErrorListener);
      if (this.socketCloseListener) this.tcpSocket.off('close', this.socketCloseListener);
    }
    this.socketMessageListener = undefined; this.socketErrorListener = undefined; this.socketCloseListener = undefined;
  }

  private failSocket(_error: Error, state: string = 'offline'): void {
    if (this.closed) return;
    const fields = _error as Object as Record<string, Object>, code = fields ? fields['code'] : undefined;
    const diagnostic = /^[a-z_0-9]{1,64}$/.test(_error.message || '') ? _error.message : typeof code === 'number' ? 'native_' + code : 'socket_failed';
    if (state !== 'closed') hilog.error(0x0000, 'PersonaNet', '%{public}s:%{public}s', this.connectionStage, diagnostic);
    this.openReject?.(_error);
    this.generation++; this.closed = true; this.upgraded = false;
    this.children.forEach((child: HubTransport) => child.close()); this.children = [];
    this.detachSocketHandlers();
    for (const task of this.requests) { try { task.cancel(); task.close(); } catch (_) { } }
    this.requests.clear();
    try { this.tlsSocket?.close().catch(() => undefined); } catch (_) { }
    try { this.tcpSocket?.close().catch(() => undefined); } catch (_) { }
    this.tlsSocket = undefined; this.tcpSocket = undefined;
    this.rx = new SocketReceiveBuffer(); this.fragmentParts = []; this.fragmentSize = 0;
    this.stateCallback?.(state, _error);
  }

  close(): void {
    if (this.closed || this.closing) return;
    this.closing = true; this.children.forEach((child: HubTransport) => child.close()); this.children = [];
    for (const task of this.requests) { try { task.cancel(); task.close(); } catch (_) {} }
    this.requests.clear(); this.openReject?.(new Error('connection_closed')); this.detachSocketHandlers();
    const finish = (): void => this.failSocket(new Error('client_closed'), 'closed');
    if (this.upgraded) this.withTimeout(this.enqueueFrame(8, new Uint8Array([0x03, 0xe8])), 500, 'close_timeout').catch(() => undefined).finally(finish);
    else finish();
  }
}
