import { withoutInputFields } from './command-input.js'
// Shared runtime history pagination (docs/integrations/runtimes.md "History").
import{RpcError}from"./wire.js";
export const HISTORY_LIMIT=200,HISTORY_MAX=500;
export function historyAt(row) {
  for (const value of [row?.active_at, row?.updated_at, row?.started_at]) {
    const timestamp = Date.parse(value);
    if (Number.isFinite(timestamp)) return timestamp;
  }
  return NaN;
}
const deleted=r=>r?.status==="deleted"||r?.deleted===!0;
const bad=m=>new RpcError(m,"invalid");
export function historyParams(p={}){const since=Date.parse(p.since);if(typeof p.since!="string"||Number.isNaN(since))throw bad("since must be an RFC 3339 time");
let until=Date.now();if(p.until!=null&&p.until!==""){until=Date.parse(p.until);if(typeof p.until!="string"||Number.isNaN(until))throw bad("until must be an RFC 3339 time")}
let limit=HISTORY_LIMIT;if(p.limit!=null){limit=Math.trunc(Number(p.limit));if(!(limit>=1))throw bad("limit must be a positive number");limit=Math.min(limit,HISTORY_MAX)}
const cursor=typeof p.cursor=="string"?p.cursor:"";return{since,until,limit,cursor}}
function decode(c){if(!c)return;try{const v=JSON.parse(Buffer.from(c,"base64url").toString());if(Number.isFinite(v.t)&&typeof v.k=="string")return v}catch{}throw bad("malformed cursor")}
/**
 * Rows in [since, until), newest first with ties by session ID.
 * @param {object[]} rows
 * @param {{since: string, until?: string, cursor?: string, limit?: number}} params
 * @param {{complete?: boolean, id?: (row: object) => string, updatedAt?: (row: object) => number}} [o]
 * @returns {{rows: object[], next_cursor: string, complete: boolean}}
 */
export function historyPage(rows,params,o={}){const{since,until,limit,cursor}=historyParams(params),after=decode(cursor),id=o.id??(r=>String(r?.session_id??"")),customTime=o.updatedAt,t=customTime??historyAt;
// A custom adapter can still return zero to signal a missing timestamp.
const list=[];for(const r of rows??[]){const k=id(r),u=t(r);if(!k||!Number.isFinite(u)||(customTime!=null&&u===0)||u<since||u>=until||deleted(r))continue;if(after&&(u>after.t||u===after.t&&k<=after.k))continue;list.push({r,u,k})}
list.sort((a,b)=>b.u-a.u||(a.k<b.k?-1:a.k>b.k?1:0));const page=list.slice(0,limit),last=page[page.length-1];
return{rows:page.map(x=>withoutInputFields(x.r)),next_cursor:list.length>limit?Buffer.from(JSON.stringify({t:last.u,k:last.k})).toString("base64url"):"",complete:o.complete!==!1}}
export function historyResult(res){return{rows:Array.isArray(res?.rows)?res.rows.filter(r=>r&&typeof r.session_id=="string"&&r.session_id&&!deleted(r)).map(withoutInputFields):[],next_cursor:typeof res?.next_cursor=="string"?res.next_cursor:"",complete:res?.complete===!0}}
