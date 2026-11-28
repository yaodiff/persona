import { commandInput, hasLiveWaits, publicSessionResult, withoutInputFields } from './command-input.js'
import{defaultListenPathMatches,wirePackageName}from"./compat.js";import{randomUUID}from"node:crypto";import{existsSync,statSync}from"node:fs";import{createServer}from"node:http";import{createConnection}from"node:net";import"node:os";import{join}from"node:path";import{envValue,socketDir}from"./identity.js";import{upgrade}from"./listen.js";import{Peer,RpcError}from"./wire.js";import{historyPage,historyParams,historyResult}from"./history.js";const PROTOCOL=1,PHONE_DRIVEN_FOR=360*60*1e3,RESUME_WAIT=120*1e3,APPEND_WINDOW=80,STREAM_EVERY=120,RETRY_PRESENT=1e3,TOUCHED=50,SEND_TTL=10*60*1e3,RETRY_MAX=3e4,NEW_NODE_EVERY=5e3,NEW_NODE_MAX=10*6e4,CONFLICT_RETRY=3e4,DEFAULT_LISTEN_PATH="/persona/runtime";function defaultSocketPath(){return envValue("PERSONA_RUNTIME_SOCKET")||join(socketDir(),"runtime.sock")}function createRuntime(driver,options){if(options?.package&&(options={...options,package:{...options.package,name:wirePackageName(options.package.name)}}),!options?.runtime)throw new Error("createRuntime: options.runtime (the runtime id) is required");const listen=options.listen?options.listen===!0?{}:options.listen:void 0,token=options.token??listen?.token;if(listen&&!token)throw new Error("createRuntime: a listening runtime needs a token (options.listen.token)");const dialing=!listen,socketPath=options.socketPath??defaultSocketPath(),answerFirst=options.answerFirst??"auto",pushDelay=options.pushDelayMs??300,baseRetry=options.retryMs??1e3,resumeWait=options.resumeWaitMs??RESUME_WAIT,log=options.log??(()=>{}),connect=options.connect??(path=>createConnection(path)),instance=typeof options.instance=="string"&&options.instance!==""?options.instance:randomUUID();let warnedConflict=!1,launchers=Array.isArray(options.launchers)?options.launchers:void 0,workspaces;const keepsWorkspaces=typeof driver.createWorkspace=="function"&&typeof driver.prepareWorkspace=="function";let peer,stopped=!1,run=0,retryTimer,retryMs=baseRetry,refreshTimer,server,pushTimer,pushing,pushAgain=!1;const listed=new Set;let unlisted;const watched=new Map,streams=new Map,pending=new Map,cancellations=new Map,phoneDriven=new Map,sends=new Map,drivenBy=id=>{for(phoneDriven.delete(id),phoneDriven.set(id,Date.now());phoneDriven.size>200;)phoneDriven.delete(phoneDriven.keys().next().value)};let hubConnected=!0,hubBacks=0,nodeFeatures=[];
// Bindings survive a transport reconnect. The local node verifies its issued value
// before dispatching input. Only the waits advertised on this connection are reachable.
const OMITTED_BINDING_TTL = 24 * 60 * 60 * 1000
const inputBindings = new Map()
let advertisedInputs = new Set()
let bindingRevision = 0
let readyPeer, inputPeer
function supportsInput(p) {
  return p === peer && p === readyPeer && p === inputPeer && !p.closed && typeof driver.input === 'function'
}
const assistantPids = new Map()
const requires=Array.isArray(options.requires)?options.requires:[];let warnedRequires=!1;function awaitNewNode(){const mine=run,id=()=>{try{const st=statSync(socketPath);return`${st.ino}:${st.mtimeMs}`}catch{return""}},was=id(),since=Date.now(),look=()=>{if(!(stopped||run!==mine)){if(id()!==was||Date.now()-since>=NEW_NODE_MAX)return open();retryTimer=setTimeout(look,NEW_NODE_EVERY),retryTimer.unref?.()}};clearTimeout(retryTimer),retryTimer=setTimeout(look,NEW_NODE_EVERY),retryTimer.unref?.()}function scheduleConnect(delay){stopped||(clearTimeout(retryTimer),retryTimer=setTimeout(open,delay),retryTimer.unref?.())}function backoff(){if(socketPresent())return retryMs=baseRetry,Math.min(baseRetry,RETRY_PRESENT);const d=retryMs;return retryMs=Math.min(retryMs*2,RETRY_MAX),d}function socketPresent(){try{return existsSync(socketPath)}catch{return!1}}function claimCodex(h){let list;try{list=typeof options.codexClaims=="function"?options.codexClaims():options.codexClaims}catch(err){log(`persona: codexClaims: ${err.message}`)}const text=v=>typeof v=="string"&&v?v:void 0,claims=Array.isArray(list)?list.map(c=>({home:text(c?.home),endpoint:text(c?.endpoint)})).filter(c=>c.home||c.endpoint):[];claims.length>0&&(h.codex_claims=claims)}function hello(){const caps={start:typeof driver.start=="function",send:typeof driver.send=="function",interrupt:typeof driver.interrupt=="function",queue_edit:typeof driver.editQueue=="function",queue_send_now:typeof driver.sendNow=="function",rename:typeof driver.rename=="function",close:typeof driver.close=="function",assistant:options.assistant===!0&&typeof driver.start=="function"&&typeof driver.send=="function"};if (dialing && typeof driver.input === "function") caps.input = true
  typeof driver.modes=="function"&&(caps.modes=!0),typeof driver.modes=="function"&&options.modelSwitch===!0&&(caps.model=!0),typeof driver.models=="function"&&(caps.models=!0),typeof driver.check=="function"&&(caps.check=!0),typeof driver.rewind=="function"&&(caps.rewind=!0),typeof driver.fork=="function"&&(caps.fork=!0),typeof driver.seen=="function"&&(caps.seen=!0),typeof driver.archive=="function"&&typeof driver.unarchive=="function"&&(caps.archive=!0),typeof driver.listHistory=="function"&&(caps.history=!0),keepsWorkspaces&&(caps.workspaces=!0),options.resume===!0&&caps.start&&(caps.resume=!0),options.keepAlways===!0&&(caps.keep_always=!0),options.readonly===!0&&caps.start&&(caps.readonly=!0),options.sameKind===!0&&caps.start&&(caps.same_kind=!0),options.supervisionPolicy===!0&&caps.assistant&&(caps.supervision_policy=!0),options.assistantEnvironment===!0&&caps.assistant&&(caps.assistant_environment=!0);const h={runtime:options.runtime,name:options.name??options.runtime,version:options.version??"",instance,caps};return options.package?.name&&options.package?.hash&&(h.package={name:options.package.name,hash:options.package.hash}),options.agent&&(h.agent=options.agent),options.launcher&&caps.start&&(h.launcher=options.launcher,options.launcherModel&&(h.launcher_model=options.launcherModel)),Array.isArray(launchers)&&caps.start&&(h.launchers=launchers),claimCodex(h),h}function open(){if(stopped)return;let socket;try{socket=connect(socketPath)}catch{scheduleConnect(backoff());return}const state={adopted:false,run};socket.once("error",()=>{}),socket.once("connect",()=>{state.adopted=true;adopt(socket,state)}),socket.once("close",()=>{state.run===run&&!state.adopted&&scheduleConnect(backoff())})}async function adopt(socket,state={}){if(dialing&&(stopped||state.run!==run)){socket.destroy();return}if(peer){const old=peer;peer=readyPeer=void 0,old.close(new Error("replaced by a newer connection from the node"))}const handshake = Promise.withResolvers()
  async function onRequest(method, params) {
    // The node registers the launcher before its hello reply reaches this socket.
    if (method === 'session.start' && params?.input_binding) {
      await handshake.promise
      if (p !== peer || p.closed) throw new RpcError('connection closed', 'closed')
    }
    return request(p, method, params)
  }
  const p=new Peer(socket,{prefix:"d",onRequest,onEvent:(event,params)=>{event==="runtime.hub"&&peer===p&&setHub(params?.connected!==!1)&&(hubChanged(),flushCancels()),event==="runtime.driven"&&peer===p&&typeof params?.session_id=="string"&&params.session_id&&drivenBy(params.session_id)}});unwatchAll(),listed.clear(),peer=p;
  advertisedInputs.clear()
  inputPeer = undefined
  nodeFeatures = []
  clearTimeout(pushTimer)
  pushing = undefined
  pushAgain = false
  try {
    const localHello = hello()
    const res = await p.call("runtime.hello", localHello, { timeoutMs: 10000 })
    if (peer !== p) return
    if (p.closed) throw new RpcError("connection closed", "closed")
    setHub(res?.hub!==!1),hubConnected&&hubBacks++,nodeFeatures=Array.isArray(res?.features)?res.features.filter(f=>typeof f=="string"):[];const missing=requires.filter(f=>!nodeFeatures.includes(f));if(missing.length>0){warnedRequires||log(`persona: this personad node lacks ${missing.join(", ")}; update personad (connecting again once it restarts)`),warnedRequires=!0,peer===p&&(peer=void 0),p.close(new Error("node too old")),dialing&&state.run===run&&awaitNewNode();return}
    if (localHello.caps.input && nodeFeatures.includes("input")) inputPeer = p
    else inputBindings.clear()
    warnedRequires=!1,warnedConflict=!1;const offer=res?.update;typeof options.onHello=="function"&&setImmediate(()=>{try{options.onHello(res)}catch(err){log(`persona: onHello: ${err.message}`)}});typeof options.onUpdate=="function"&&options.package&&offer?.name===options.package.name&&typeof offer.hash=="string"&&offer.hash!==options.package.hash&&setImmediate(()=>{try{options.onUpdate({name:offer.name,hash:offer.hash,version:typeof offer.version=="string"?offer.version:""})}catch(err){log(`persona: onUpdate: ${err.message}`)}})}catch(err){if(peer!==p)return;peer=void 0;if(err instanceof RpcError&&err.code==="runtime_conflict"){warnedConflict||log(`persona: ${err.message}; give this one another runtime id (trying again every ${CONFLICT_RETRY/1e3}s)`),warnedConflict=!0,p.close(err),dialing&&state.run===run&&scheduleConnect(CONFLICT_RETRY);return}log(`persona: hello failed: ${err.message}`),p.close(err),dialing&&state.run===run&&scheduleConnect(backoff());return}finally{handshake.resolve()}readyPeer=p,retryMs=baseRetry,log("persona: connected to the node"),workspaces&&keepsWorkspaces&&p.notify("runtime.workspaces",{workspaces}),schedulePush(0),hubChanged(),flushCancels();const why=await p.done;if(peer!==p)return;peer=readyPeer=inputPeer=void 0;log(`persona: node connection closed${why?.message?`: ${why.message}`:""}`),dialing&&state.run===run&&scheduleConnect(baseRetry)}function accept(req,socket,head){if(!listen)throw new Error("accept: this runtime dials runtime.sock (no options.listen)");if(stopped)return socket.destroy(),!1;let expected;try{expected=typeof token=="function"?token():token}catch{expected=void 0}const conn=upgrade(req,socket,head,{token:expected});return conn?(adopt(conn),!0):(log("persona: refused a connection (wrong token, or not the runtime protocol)"),!1)}function serve(){const path=listen.path??DEFAULT_LISTEN_PATH;return server=createServer((req,res)=>{res.writeHead(404,{"content-type":"text/plain"}),res.end("Not Found")}),server.on("upgrade",(req,socket,head)=>{let pathname="";try{pathname=new URL(req.url??"/","http://localhost").pathname}catch{}if(!defaultListenPathMatches(pathname,path,DEFAULT_LISTEN_PATH)){socket.end(`HTTP/1.1 404 Not Found\r
Connection: close\r
Content-Length: 0\r
\r
`);return}accept(req,socket,head)}),new Promise((resolve,reject)=>{server.once("error",reject),server.listen(listen.port??0,listen.host??"127.0.0.1",()=>{server.off("error",reject),server.on("error",err=>log(`persona: endpoint: ${err.message}`)),resolve(server.address())})})}function schedulePush(delay=pushDelay){peer&&(clearTimeout(pushTimer),pushTimer=setTimeout(pushSessions,delay),pushTimer.unref?.())}async function listNow() {
  const destination = peer
  clearTimeout(pushTimer)
  while (peer === destination && pushing) await pushing
  if (peer === destination) await pushSessions()
}

function pushSessions() {
  if (!peer) return Promise.resolve()
  if (pushing) {
    pushAgain = true
    return pushing
  }
  const pendingPush = push(peer).finally(() => {
    if (pushing !== pendingPush) return
    pushing = undefined
    if (pushAgain) {
      pushAgain = false
      schedulePush()
    }
  })
  pushing = pendingPush
  return pendingPush
}

async function push(destination) {
  try {
    const revision = bindingRevision
    const includeInputs = supportsInput(destination)
    const rows = await driver.listSessions()
    if (peer !== destination) return
    if (typeof driver.unlistedTranscripts === 'function') {
      try {
        const named = await driver.unlistedTranscripts()
        if (peer !== destination) return
        if (Array.isArray(named)) unlisted = named.filter(id => typeof id === 'string' && id)
      } catch (err) {
        log(`persona: unlisted transcripts: ${err.message}`)
      }
    }
    if (peer !== destination) return
    if (revision !== bindingRevision) return schedulePush(0)
    const list = []
    const reported = new Set()
    for (const row of rows ?? []) {
      if (!row?.session_id) continue
      let session = withoutInputFields(row)
      if (row.deleted || row.status === 'deleted') inputBindings.delete(row.session_id)
      const binding = inputBindings.get(row.session_id)
      if (binding && includeInputs && supportsInput(destination) && hasLiveWaits(row)) {
        session = { ...session, waiting_inputs: row.waiting_inputs, input_binding: binding.value }
        reported.add(row.session_id)
      }
      list.push((pending.get(row.session_id) ?? 0) > 0 ? { ...session, status: 'blocked' } : session)
    }
    listed.clear()
    for (const session of list) listed.add(session.session_id)
    if (destination.notify('runtime.sessions', unlisted ? { sessions: list, unlisted_transcripts: unlisted } : { sessions: list })) {
      advertisedInputs = reported
      // Some drivers retire sessions by omission alone. Allow temporary gaps in their inventory.
      const now = Date.now()
      for (const [id, binding] of inputBindings) {
        if (listed.has(id)) binding.missingSince = undefined
        else if (binding.missingSince === undefined) binding.missingSince = now
        else if (now - binding.missingSince >= OMITTED_BINDING_TTL) inputBindings.delete(id)
      }
    }
    reportAssistantPids()
  } catch (err) {
    log(`persona: list sessions: ${err.message}`)
  }
}
async function cursorOf(sessionId){const at=typeof driver.cursor=="function"?await driver.cursor(sessionId):await driver.items({session_id:sessionId,turns:1});return typeof at=="number"?{seq:at}:{seq:at?.to_seq??0,history:at?.history}}async function watch(params){const id=params.session_id,since=typeof params.since_seq=="number"?params.since_seq:0,prev=watched.get(id);if(prev&&clearTimeout(prev.timer),params.transcript===!0)return watched.set(id,{last:0,catchingUp:!1,held:[],batch:void 0,timer:void 0,transcript:!0}),watchChanged(id,!0,{transcript:!0}),{ok:!0};const w={last:since,history:params.history||prev?.history,catchingUp:!0,held:[],batch:void 0,timer:void 0};watched.set(id,w),watchChanged(id,!0,{transcript:!1});try{if(since>0){let after=since;for(let i=0;i<8;i++){let page=await driver.items({session_id:id,after_seq:after});if(watched.get(id)!==w)return{ok:!0};
const historyChanged = !!page.history && w.history !== void 0 && page.history !== w.history;
if((page.items?.length??0)>0||page.to_seq!==w.last||historyChanged||page.from_seq===0){const moreAfter=i===7&&page.more_after===!0;peer?.notify("session.items",{session_id:id,items:page.items??[],history:page.history,from_seq:page.from_seq??after,to_seq:page.to_seq,...moreAfter?{more_after:!0}:{}}),w.last=page.to_seq}if(page.history!==void 0&&(w.history=page.history),!page.more_after)break;after=page.to_seq}}else{const at=await cursorOf(id);w.last=at.seq,w.history=at.history}}catch(err){log(`persona: catch up ${id}: ${err.message}`)}finally{w.catchingUp=!1;const held=w.held;w.held=[];for(const a of held)queue(id,w,a);if(w.pendingStream){const s=w.pendingStream;w.pendingStream=void 0,sendStream(id,s)}}return{ok:!0}}function unwatch(params){const w=watched.get(params.session_id);return w&&clearTimeout(w.timer),watched.delete(params.session_id),w&&watchChanged(params.session_id,!1),{ok:!0}}function unwatchAll(){for(const[id,w]of watched)clearTimeout(w.timer),watchChanged(id,!1);watched.clear()}function watchChanged(id,watching,o){if(typeof driver.watchChanged=="function")try{driver.watchChanged(id,watching,o)}catch(err){log(`persona: watchChanged ${id}: ${err.message}`)}}function append(sessionId,{items=[],fromSeq,toSeq,history,update=!1}={}){if(typeof toSeq!="number")throw new Error("append needs toSeq");const w=watched.get(sessionId);if(!w){listed.has(sessionId)&&schedulePush(2e3);return}if(w.transcript)return;const a={items,fromSeq:typeof fromSeq=="number"?fromSeq:void 0,toSeq,history,update:update===!0};if(w.catchingUp){w.held.push(a);return}queue(sessionId,w,a)}function mergeBatchItems(dst,src){for(const it of src){const idx=it?.id!==void 0?dst.findIndex(old=>old?.id===it.id):-1;idx>=0?dst[idx]=it:dst.push(it)}}function rewrites(w,a){const current=w.batch?w.batch.history:w.history;return a.history!==void 0&&current!==void 0&&a.history!==current}function queue(id, w, a) {
  const rewritten = a.fromSeq === 0 || rewrites(w, a)
  const inPlace = !rewritten && a.update && a.items.length > 0
  if (!rewritten && !inPlace && a.toSeq <= w.last && w.batch === undefined) return
  const batch = w.batch
  if (batch && !rewritten && (a.fromSeq === undefined || a.fromSeq === batch.toSeq) && a.history === batch.history) {
    mergeBatchItems(batch.items, a.items)
    batch.toSeq = Math.max(batch.toSeq, a.toSeq)
    batch.update ||= a.update
  } else {
    if (batch) flush(id, w)
    w.batch = { items: [...a.items], fromSeq: a.fromSeq ?? (rewritten ? undefined : w.last),
      toSeq: rewritten ? a.toSeq : Math.max(a.toSeq, w.last), history: a.history, rewritten, update: a.update }
  }
  // A wake opens an exchange. Deliver its boundary before live text can replace the previous answer.
  if (a.items.some(item => item.wake === true)) flush(id, w)
  else if (!w.timer) {
    w.timer = setTimeout(() => flush(id, w), APPEND_WINDOW)
    w.timer.unref?.()
  }
}
function flush(id,w){clearTimeout(w.timer),w.timer=void 0;const b=w.batch;if(w.batch=void 0,!b||watched.get(id)!==w||!peer)return;const msg={session_id:id,items:b.items,to_seq:b.toSeq};b.fromSeq!==void 0&&(msg.from_seq=b.fromSeq),b.history!==void 0&&(msg.history=b.history),b.update&&(msg.update=!0),peer.notify("session.items",msg),w.last=b.rewritten?b.toSeq:Math.max(w.last,b.toSeq),b.history!==void 0&&(w.history=b.history)}function sendStream(id,s){const w=watched.get(id);if(!w||streams.get(id)!==s||!peer)return;if(w.catchingUp){w.pendingStream=s;return}w.batch&&flush(id,w),s.sentAt=Date.now(),peer.notify("session.delta",{session_id:id,text:s.text})}function stream(sessionId,text){if(!watched.has(sessionId)||!peer)return;let s=streams.get(sessionId);if(s||(s={text:"",timer:void 0,sentAt:0},streams.set(sessionId,s)),s.text=text,s.timer)return;const wait=Math.max(0,s.sentAt+STREAM_EVERY-Date.now());s.timer=setTimeout(()=>{s.timer=void 0,sendStream(sessionId,s)},wait),s.timer.unref?.()}function streamEnd(sessionId){const s=streams.get(sessionId),w=watched.get(sessionId);s&&clearTimeout(s.timer),streams.delete(sessionId),w&&(w.pendingStream=void 0),(s||w)&&(w&&!w.catchingUp&&w.batch&&flush(sessionId,w),peer?.notify("session.delta",{session_id:sessionId,text:""}))}function compactionSettled(sessionId,error){return typeof sessionId=="string"&&sessionId?!!peer?.notify("session.compaction.settled",error?{session_id:sessionId,error:String(error)}:{session_id:sessionId}):!1}
function reportAssistantPids() {
  return supportsAssistantPids() && peer && peer === readyPeer && nodeFeatures.includes('assistant_pids')
    ? peer.notify('runtime.assistant_pids', { pids: Object.fromEntries(assistantPids) })
    : false
}
function supportsAssistantPids() {
  return options.assistant === true && typeof driver.start === 'function' && typeof driver.send === 'function'
}
function assistantPid(sessionId, pid) {
  if (!supportsAssistantPids()) return false
  if (typeof sessionId !== 'string' || !sessionId || !Number.isSafeInteger(pid) || pid < 0) {
    throw new TypeError('assistantPid needs a session id and a nonnegative integer PID')
  }
  if (pid) assistantPids.set(sessionId, pid)
  else assistantPids.delete(sessionId)
  return reportAssistantPids()
}
function usage(report) {
  const { session_id, ts, model, request_id, input = 0, output = 0, cache_read = 0, cache_write = 0, reasoning = 0 } = report ?? {}
  if (typeof session_id !== 'string' || !session_id || typeof model !== 'string' || !model || Buffer.byteLength(model) > 256 || typeof ts !== 'string' || !/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d{1,9})?(?:Z|[+-]\d{2}:\d{2})$/.test(ts) || !Number.isFinite(Date.parse(ts)) || request_id !== undefined && (typeof request_id !== 'string' || Buffer.byteLength(request_id) > 512)) throw new TypeError('usage needs session_id, model and an RFC3339 ts')
  const counts = [input, output, cache_read, cache_write]
  if (!counts.every(v => Number.isSafeInteger(v) && v >= 0) || !Number.isSafeInteger(counts.reduce((a, b) => a + b, 0)) || !Number.isSafeInteger(reasoning) || reasoning < 0 || reasoning > output) throw new TypeError('usage needs nonnegative safe integer counts; reasoning is part of output')
  if (!peer || !hubConnected || !nodeFeatures.includes('usage')) return false
  return peer.notify('usage.report', { session_id, ts, model, ...(request_id === undefined ? {} : { request_id }), input, output, cache_read, cache_write, reasoning })
}
function usageWindows(report) {
  const { provider, name, plan, account_key: accountKey, sampled_at: sampledAt, windows } = report ?? {}
  const timestamp = (value) => typeof value === 'string' && /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d{1,9})?(?:Z|[+-]\d{2}:\d{2})$/.test(value) && Number.isFinite(Date.parse(value))
  const text = (value, max) => typeof value === 'string' && value.length > 0 && Buffer.byteLength(value) <= max
  if (!text(provider, 64) || !text(name, 128) || !timestamp(sampledAt) || !Array.isArray(windows) || windows.length > 32 ||
      plan !== undefined && (typeof plan !== 'string' || Buffer.byteLength(plan) > 128) ||
      accountKey !== undefined && !text(accountKey, 128)) {
    throw new TypeError('usageWindows needs provider, name, sampled_at and at most 32 windows')
  }
  const normalized = windows.map((window) => {
    const { label, window_minutes: minutes, used_percent: used, resets_at: resetsAt, model } = window ?? {}
    if (!text(label, 32) || !Number.isSafeInteger(minutes) || minutes <= 0 ||
        typeof used !== 'number' || !Number.isFinite(used) || used < 0 || used > 100 ||
        resetsAt !== undefined && !timestamp(resetsAt) || model !== undefined && (typeof model !== 'string' || Buffer.byteLength(model) > 256)) {
      throw new TypeError('usageWindows needs a positive window length, a 0..100 percentage and RFC3339 times')
    }
    return { label, window_minutes: minutes, used_percent: used,
      ...(resetsAt === undefined ? {} : { resets_at: resetsAt }), ...(model ? { model } : {}) }
  })
  if (!peer || !nodeFeatures.includes('usage_windows')) return false
  return peer.notify('usage.windows', { provider, name, ...(plan ? { plan } : {}), ...(accountKey ? { account_key: accountKey } : {}), sampled_at: sampledAt, windows: normalized })
}
function setHub(connected){return connected===hubConnected?!1:(hubConnected=connected,connected&&hubBacks++,log(`persona: the node's hub ${connected?"is back":"is gone"}`),!0)}function hubChanged(){if(hubConnected)reportAssistantPids();if(typeof options.onHub=="function")try{options.onHub(peer!==void 0&&hubConnected)}catch(err){log(`persona: onHub: ${err.message}`)}}function phoneFirst(sessionId){if(!peer||!hubConnected)return!1;if(answerFirst==="phone")return!0;if(answerFirst==="local")return!1;const at=phoneDriven.get(sessionId);return at!==void 0&&Date.now()-at<PHONE_DRIVEN_FOR?!0:listed.has(sessionId)?typeof driver.hasLocalClient=="function"&&driver.hasLocalClient(sessionId)===!1:!1}function waitingOnUser(sessionId){pending.set(sessionId,(pending.get(sessionId)??0)+1),schedulePush(50);let released=!1;return()=>{released||(released=!0,pending.set(sessionId,Math.max(0,(pending.get(sessionId)??1)-1)),schedulePush(50))}}function flushCancels(){if(!peer||!hubConnected)return;for(const id of cancellations.keys()){if(!peer.notify("interaction.cancel",{id}))break;cancellations.delete(id)}}function cancelInteraction(id){if(!id)return;for(cancellations.set(id,!0);cancellations.size>200;)cancellations.delete(cancellations.keys().next().value);flushCancels()}async function ask(interaction,{signal,timeoutSeconds}={}){const sessionId=interaction.session_id;pending.set(sessionId,(pending.get(sessionId)??0)+1),schedulePush(50);try{let lost;const resumable=()=>((!lost||lost.backs!==hubBacks)&&(lost={at:Date.now(),backs:hubBacks}),Date.now()-lost.at<resumeWait);for(;;){if(signal?.aborted)throw signal.reason??new Error("aborted");const p=peer;if(!p){if(!resumable())return{dismiss:!0};await sleep(1e3,signal);continue}try{listed.has(sessionId)||await listNow();const params={interaction};return timeoutSeconds&&(params.timeout_seconds=timeoutSeconds),await p.call("interaction.create",params,{timeoutMs:0,signal})}catch(err){if(signal?.aborted)throw err;if(err instanceof RpcError&&(err.code==="node_gone"||err.code==="closed")&&resumable()){await sleep(1e3,signal);continue}return log(`persona: ask the phone: ${err.message}`),{dismiss:!0}}}}catch(err){throw signal?.aborted&&cancelInteraction(interaction.id),err}finally{pending.set(sessionId,Math.max(0,(pending.get(sessionId)??1)-1)),schedulePush(50)}}async function request(source, method, params) {
  let binding
  if (Object.hasOwn(params, 'input_binding')) {
    const { input_binding, ...driverParams } = params
    binding = input_binding
    params = driverParams
  }
  const need=(fn,what)=>{if(typeof fn!="function")throw new RpcError(`unsupported: ${what}`,"unsupported");return fn};switch(method){case"sessions.history":{need(driver.listHistory,"history");const q=historyParams(params);return historyResult(await driver.listHistory({since:params.since,until:params.until||new Date(q.until).toISOString(),cursor:q.cursor,limit:q.limit}))}case"session.items":return driver.items(params);case"session.image":return need(driver.image,"image")(params);case"session.item":return need(driver.item,"item")(params);case"session.watch":return watch(params);case"session.unwatch":return unwatch(params);case "session.input": {
  if (!supportsInput(source)) throw new RpcError("command_input_unreachable", "command_input_unreachable")
  if (typeof params.session_id !== 'string' || !params.session_id) throw new RpcError("prompt_changed", "prompt_changed")
  if (!inputBindings.has(params.session_id) || !advertisedInputs.has(params.session_id)) {
    throw new RpcError("command_input_unreachable", "command_input_unreachable")
  }
  const result = await commandInput(driver, params)
  schedulePush()
  return result
}
case"session.send":case"session.send_unseen":{need(driver.send,"send"),drivenBy(params.session_id);const key=typeof params.client_msg_id=="string"&&params.client_msg_id?params.client_msg_id:void 0,replacing=method==="session.send_unseen",cached=key&&sends.get(key);if(cached){if(replacing&&cached.done&&cached.unknown||!replacing&&cached.done&&Date.now()-cached.at>SEND_TTL)sends.delete(key);else return cached.p}const p=(async()=>{const r=await driver.send(params);return schedulePush(),r?.queued===!0?{ok:!0,queued:!0}:{ok:!0}})();if(key){const entry={p,at:Date.now(),done:!1,unknown:!1};sends.set(key,entry),p.then(()=>{entry.done=!0},err=>{entry.done=!0,entry.unknown=err?.data?.delivered==="unknown",!entry.unknown&&sends.get(key)===entry&&sends.delete(key)});for(;sends.size>500;)sends.delete(sends.keys().next().value)}return p}case"session.queue.edit":{const result=await need(driver.editQueue,"queue_edit")(params);return schedulePush(),result}case"session.queue.send_now":return await need(driver.sendNow,"queue_send_now")(params),schedulePush(),{sent:!0};case"session.interrupt":return(await need(driver.interrupt,"interrupt")(params))?.settled===!0?{ok:!0,settled:!0}:{ok:!0};case"session.fork":return publicSessionResult(await need(driver.fork,"fork")(params));case"session.rewind":{const result=await need(driver.rewind,"rewind")(params);return schedulePush(),result}case"session.rename":return await need(driver.rename,"rename")(params),schedulePush(),{ok:!0};case"session.seen":return await need(driver.seen,"seen")(params),{ok:!0};case "session.close": {
  const id = params.session_id
  const binding = inputBindings.get(id)
  await need(driver.close, "close")(params)
  if (inputBindings.get(id) === binding && inputBindings.delete(id)) bindingRevision++
  schedulePush()
  return { ok: true }
}case"session.archive":case"session.unarchive":return typeof driver.archive=="function"&&typeof driver.unarchive=="function"||need(void 0,"archive"),await(method==="session.archive"?driver.archive:driver.unarchive)(params),schedulePush(),{ok:!0};case"launchers.check":return need(driver.check,"check")(params);case"launchers.models":return need(driver.models,"models")(params);case"session.modes":{const r=await need(driver.modes,"modes")(params);return schedulePush(),r?.permission_mode?{ok:!0,permission_mode:r.permission_mode}:{ok:!0}}case "session.start": {
  const requested = params.session_id
  const bind = supportsInput(source) && typeof requested === 'string' && requested &&
    typeof binding === 'string' && binding.length > 0
  const early = new Set()
  try {
    const res = await need(driver.start, "start")(params, id => {
      if (typeof id === 'string' && id) {
        early.add(id)
        drivenBy(id)
      }
    })
    const reply = publicSessionResult(res)
    const id = reply?.session?.session_id
    if (id) drivenBy(id)
    if (bind && id === requested && supportsInput(source)) {
      bindingRevision++
      inputBindings.set(id, { value: binding, missingSince: undefined })
      advertisedInputs.delete(id)
    }
    schedulePush()
    return reply
  } catch (err) {
    for (const id of early) phoneDriven.delete(id)
    throw err
  }
}case"workspace.prepare":return need(keepsWorkspaces&&driver.prepareWorkspace,"workspaces")(params);case"workspace.create":return need(keepsWorkspaces&&driver.createWorkspace,"workspaces")(params);case"workspace.rename":return need(keepsWorkspaces&&driver.createWorkspace,"workspaces"),need(driver.renameWorkspace,"workspace.rename")(params);case"workspace.archive":return need(keepsWorkspaces&&driver.createWorkspace,"workspaces"),need(driver.archiveWorkspace,"workspace.archive")(params)}throw new RpcError(`unknown method ${method}`,"unsupported")}return{start(){return stopped=!1,options.refreshMs!==0&&(refreshTimer=setInterval(()=>schedulePush(),options.refreshMs??3e4),refreshTimer.unref?.()),dialing?(open(),Promise.resolve(void 0)):typeof listen.port=="number"?serve():Promise.resolve(void 0)},accept,address:()=>server?.address()??void 0,stop(){stopped=!0,run++,assistantPids.clear(),readyPeer=inputPeer=void 0,server?.close(),server=void 0,clearTimeout(retryTimer),clearTimeout(pushTimer),clearInterval(refreshTimer),unwatchAll();for(const s of streams.values())clearTimeout(s.timer);streams.clear(),peer?.close(new Error("runtime stopped")),peer=void 0},sessionsChanged:delayMs=>schedulePush(delayMs),launchersChanged(list){launchers=Array.isArray(list)?list:[],typeof driver.start=="function"&&peer?.notify("runtime.launchers",{launchers})},workspacesChanged(list){workspaces=Array.isArray(list)?list:[],keepsWorkspaces&&peer?.notify("runtime.workspaces",{workspaces})},append,stream,streamEnd,compactionSettled,usage,usageWindows,assistantPid,ask,phoneFirst,waitingOnUser,watching:sessionId=>watched.has(sessionId),touched(id){return[...phoneDriven.keys()].slice(-TOUCHED).includes(id)},history:(rows,params,o)=>historyPage(rows,params,o),recent(rows,o={}){const days=o.days??7,max=o.max??80,id=o.id??(r=>r.session_id),at=o.updatedAt??(r=>Date.parse(r.updated_at)||0),pinned=o.pinned??(()=>!1),touched=new Set([...phoneDriven.keys()].slice(-TOUCHED)),since=Date.now()-days*24*60*60*1e3,sorted=[...rows].sort((a,b)=>at(b)-at(a)),always=sorted.filter(r=>pinned(r)||touched.has(id(r))),rest=sorted.filter(r=>!pinned(r)&&!touched.has(id(r))&&at(r)>=since).slice(0,Math.max(0,max-always.length));return[...always,...rest].sort((a,b)=>at(b)-at(a))},get connected(){return peer!==void 0},get ready(){return readyPeer!==void 0},nodeHas:feature=>peer!==void 0&&nodeFeatures.includes(feature),get hubConnected(){return peer!==void 0&&hubConnected},
async tools({ assistant = false, leaderOnly = false, sessionId, signal } = {}) {
  const p = peer;
  if (!p) return [];
  if (sessionId && !listed.has(sessionId)) await listNow();
  const params = {};
  if (assistant) params.assistant = true;
  if (leaderOnly) params.leader_only = true;
  if (sessionId) params.session_id = sessionId;
  const res = await p.call("mcp.tools", params, { timeoutMs: leaderOnly ? 2000 : 15000, signal });
  return Array.isArray(res?.tools) ? res.tools.filter(t => t && typeof t.name === "string" && t.name !== "") : [];
},
async callTool(params, { signal } = {}) {
  const p = peer;
  if (!p) return { content: [{ type: "text", text: "Persona is not connected on this machine" }], is_error: true };
  const sessionId = params.session_id ?? "";
  if (nodeFeatures.includes("leader_tools") && sessionId && !listed.has(sessionId)) await listNow();
  const call = { name: params.name, args: params.args ?? {}, session_id: sessionId };
  if (params.assistant) call.assistant = true;
  return p.call("mcp.call", call, { timeoutMs: 0, signal });
},
async a2aMcpToken(sessionId) {
  const p = peer;
  if (!p || !nodeFeatures.includes("a2a_mcp")) return undefined;
  const res = await p.call("runtime.a2a_mcp", { session_id: sessionId }, { timeoutMs: 2000 });
  return typeof res?.token === "string" ? res.token : undefined;
},
busy:()=>[...pending.values()].some(n=>n>0),async fetchPackage(name){name=wirePackageName(name);const p=peer;if(!p)throw new Error("not connected to the node");return p.call("runtime.package",{name},{timeoutMs:6e4})},pushNow:pushSessions}}function sleep(ms,signal){return new Promise((resolve,reject)=>{const t=setTimeout(resolve,ms);signal?.addEventListener("abort",()=>{clearTimeout(t),reject(signal.reason??new Error("aborted"))},{once:!0})})}export{DEFAULT_LISTEN_PATH,PROTOCOL,createRuntime,defaultSocketPath};
