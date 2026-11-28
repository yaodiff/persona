var c=/^agent execution terminated due to error\.?$/i,R=/^(?:api error:\s*)?the stream was interrupted\.?(?:\s+please continue\b.*)?$/is;function _(r){return typeof r=="string"&&R.test(r.trim())}function f(r){return r?.type==="ERROR_MESSAGE"&&_(r.error||r.content)}function S(r,t){if(r?.type!=="CORTEX_STEP_TYPE_ERROR_MESSAGE"||["CORTEX_STEP_STATUS_CANCELLED","CORTEX_STEP_STATUS_CANCELED"].includes(r.status))return null;let e=r.errorMessage?.error;if(!e)return null;let o=[e.shortError,e.modelErrorMessage].find((i)=>typeof i=="string"&&i.trim()),n=e.userErrorMessage||e.modelErrorMessage||e.shortError;if(typeof n=="string"&&c.test(n.trim())&&o&&(n=o),typeof n!="string"||!n.trim()||_(n))return null;let s={turn_error:n.trim()},a;try{a=typeof e.details=="string"?JSON.parse(e.details):e.details}catch{}let E=Array.isArray(a?.error?.details)?a.error.details:[],u=E.find((i)=>i?.reason==="QUOTA_EXHAUSTED")?.metadata?.quotaResetTimeStamp;return typeof u=="string"&&Number.isFinite(Date.parse(u))&&(s.turn_error_kind="usage_limit",s.turn_error_resets_at=new Date(u).toISOString()),d(s,Number(e.errorCode),E),{step_index:t,type:"ERROR_MESSAGE",status:"DONE",created_at:r.metadata?.createdAt,content:s.turn_error,turn_error:s}}function d(r,t,e=[]){if(t===429&&(r.turn_error_code="rate_limited"),t>=400&&t<500&&![401,408,429].includes(t)&&!r.turn_error_kind){r.turn_error_kind="user";let o=e.find((n)=>typeof n?.reason=="string"&&n.reason)?.reason;o&&!r.turn_error.includes(o)&&(r.turn_error+=` (${o})`)}}var l=/^⚠ ([A-Z][A-Z_]* \(code (\d{3})\): [^\n]+)$/;function m(r){let t=typeof r=="string"&&l.exec(r.trim());if(!t)return null;let e={turn_error:t[1]};return d(e,Number(t[2])),e}export{f as isInterruptedErrorStep,_ as isInterruptedErrorText,S as nativeErrorStep,m as streamedErrorInfo};

// Inspect only protocol error discriminators; never copy provider payloads into recovery errors.
function isAuthenticationRequired(error) {
  return [error?.code, error?.message, error?.data?.code, error?.data?.reason, error?.data?.error?.code]
    .some(value => typeof value === "string" && /\bauth(?:entication)?[ _-]required\b/i.test(value));
}
function nativeAuthError(reason) {
  const messages = {
    failed: "Native Antigravity authentication refresh failed. Sign in with the CLI before retrying.",
    timeout: "Native Antigravity authentication refresh timed out. Sign in with the CLI before retrying.",
    cooldown: "Native Antigravity authentication refresh is cooling down after a failed attempt. Try again later.",
    retry_exhausted: "Native Antigravity still requires authentication after refresh. Sign in with the CLI before retrying.",
    prompt_started: "Antigravity requires authentication after prompt activity. The prompt was not replayed.",
    connection_changed: "Antigravity connection changed during authentication refresh. The request was not replayed.",
    cancelled: "Antigravity prompt was cancelled during authentication refresh. The prompt was not replayed.",
  };
  return Object.assign(new Error(messages[reason]), { code: "auth_required", authRecovery: reason, noPromptReplay: true });
}
export { isAuthenticationRequired, nativeAuthError };
