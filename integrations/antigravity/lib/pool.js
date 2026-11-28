import { nativeAuthError } from "./errors.js";
import{API_KEY_METHOD,geminiKeyMode}from"./provider.js";const NATIVE_UNSET=["GEMINI_API_KEY","GOOGLE_GEMINI_BASE_URL","PERSONA_AGY_MODEL_PROVIDER"];function channelKey(spec){return`${spec.id}\0${spec.authMode}\0${spec.rev}`}function normalizeChannel(raw,fallbackEnv,agyDir){if(raw&&typeof raw=="object"){const env=raw.env&&typeof raw.env=="object"?{...raw.env}:null,id=typeof raw.id=="string"&&raw.id.trim()?raw.id.trim():"native",hasKey=!!(env&&String(env.GEMINI_API_KEY||"").trim()),authRaw=(typeof raw.auth_mode=="string"?raw.auth_mode:typeof raw.authMode=="string"?raw.authMode:"").trim().toLowerCase(),keyMode=authRaw?authRaw===API_KEY_METHOD||authRaw==="gemini":hasKey,spec={id,authMode:keyMode?API_KEY_METHOD:"oauth",rev:typeof raw.rev=="string"?raw.rev.trim():"",env,keyMode};return spec.key=channelKey(spec),spec}const keyMode=geminiKeyMode({env:fallbackEnv,agyDir}),spec={id:"native",authMode:keyMode?API_KEY_METHOD:"oauth",rev:"",env:null,keyMode};return spec.key=channelKey(spec),spec}class AgyAcpPool{constructor({defaultSpec,make}){this.defaultSpec=defaultSpec,this.make=make,this.pools=new Map,this.latestByChannel=new Map,this.sessionPools=new Map,this.defaultAcp=make(defaultSpec),this.pools.set(defaultSpec.key,this.defaultAcp),this.latestByChannel.set(defaultSpec.id,defaultSpec.key)}get(spec){const s=spec||this.defaultSpec;let acp=this.pools.get(s.key);return acp||(acp=this.make(s),this.pools.set(s.key,acp)),s.id!=="native"&&this.latestByChannel.set(s.id,s.key),acp}bindSession(rawId,spec){rawId&&spec&&this.sessionPools.set(rawId,spec.key)}poolKeyOf(rawId){return this.sessionPools.get(rawId)||this.defaultSpec.key}forSession(rawId){const key=this.sessionPools.get(rawId);return(key&&this.pools.get(key))||this.defaultAcp}isSuperseded(key,acp){const id=acp?.channelSpec?.id;return!!(id&&id!=="native"&&this.latestByChannel.get(id)&&this.latestByChannel.get(id)!==key)}hasActiveExtra(){for(const[key,acp]of this.pools)if(key!==this.defaultSpec.key&&(acp.proc||acp.recycling||this.isSuperseded(key,acp)))return!0;return!1}entries(){return this.pools.entries()}values(){return this.pools.values()}delete(key,acp){this.pools.get(key)===acp&&this.pools.delete(key);for(const[rawId,k]of this.sessionPools)k===key&&this.sessionPools.delete(rawId)}}export{AgyAcpPool,NATIVE_UNSET,channelKey,normalizeChannel};

// One coordinator per channel pool, shared by its regular and dedicated assistant connections.
// A successful refresh updates the keyring; existing ACP processes read it on their next request.
class NativeOAuthRecovery {
  constructor({ timeoutMs = 15000, cooldownMs = 60000, now = Date.now } = {}) {
    this.timeoutMs = timeoutMs;
    this.cooldownMs = cooldownMs;
    this.now = now;
    this.generation = 0;
    this.retryAt = 0;
    this.flight = null;
  }
  fail(reason, log) {
    this.retryAt = this.now() + this.cooldownMs;
    log(JSON.stringify({ event: "agy_native_auth_refresh", outcome: reason, cooldownMs: this.cooldownMs }));
    return nativeAuthError(reason);
  }
  async refresh(generation, authenticate, log) {
    if (this.flight) return this.flight;
    if (this.now() < this.retryAt) {
      log(JSON.stringify({ event: "agy_native_auth_refresh", outcome: "cooldown" }));
      throw nativeAuthError("cooldown");
    }
    // A concurrent request may receive its old auth failure after another connection refreshed.
    if (generation !== this.generation) return;
    this.flight = (async () => {
      log(JSON.stringify({ event: "agy_native_auth_refresh", outcome: "started", timeoutMs: this.timeoutMs }));
      try {
        await authenticate(this.timeoutMs);
        this.generation++;
        this.retryAt = 0;
        log(JSON.stringify({ event: "agy_native_auth_refresh", outcome: "succeeded" }));
      } catch (error) {
        throw this.fail(error?.acpTimeout ? "timeout" : "failed", log);
      }
    })();
    try { return await this.flight; } finally { this.flight = null; }
  }
}
export { NativeOAuthRecovery };

function isNativeOAuthChannel(spec) {
  return spec?.id === "native" && spec.authMode === "oauth" && !spec.keyMode &&
    !["GEMINI_API_KEY", "GOOGLE_GEMINI_BASE_URL", "PERSONA_AGY_MODEL_PROVIDER"]
      .some(key => spec.env?.[key] != null && String(spec.env[key]).trim());
}
export { isNativeOAuthChannel };
