import{createRuntime,defaultSocketPath,DEFAULT_LISTEN_PATH,PROTOCOL}from"./lib/runtime.js";import{SUBPROTOCOL,tokenMatches,upgrade}from"./lib/listen.js";import{Peer,RpcError}from"./lib/wire.js";import{PAGE_TEXT,PAGE_OUTPUT,MAX_INPUT,appToolName,clipTo,clipInput,light,page,snippet,summaryOf,interactionId}from"./lib/items.js";import{createLoader,LOADER_API}from"./lib/loader.js";import{HISTORY_LIMIT,HISTORY_MAX,historyPage}from"./lib/history.js";import{PACKAGES,packageHash,readShipped,readTree,storeVersion,validPath,verifyPackage}from"./lib/package.js";import{currentHash,installCurrent,registerActiveVersion,selfPackage}from"./lib/package.js";export{DEFAULT_LISTEN_PATH,HISTORY_LIMIT,HISTORY_MAX,historyPage,LOADER_API,MAX_INPUT,PACKAGES,PAGE_OUTPUT,PAGE_TEXT,PROTOCOL,Peer,RpcError,SUBPROTOCOL,appToolName,clipInput,clipTo,createLoader,createRuntime,currentHash,defaultSocketPath,installCurrent,interactionId,light,packageHash,page,readShipped,readTree,registerActiveVersion,selfPackage,snippet,storeVersion,summaryOf,tokenMatches,upgrade,validPath,verifyPackage};

/**
 * One wait for a live session started with the ID requested by a local node that negotiated input.
 * The SDK privately binds the session after start succeeds. Allocate a fresh opaque
 * wait_id when the command starts waiting, keep it across snapshots, and call
 * sessionsChanged() when a wait appears, changes or ends.
 * @typedef {{task: string, wait_id: string, command?: string, prompt: string, output?: string, masked?: boolean}} WaitingInput
 */

/** @typedef {{session_id: string, waiting_inputs?: WaitingInput[]}} InputSessionRow */
/** @typedef {'prompt_changed' | 'command_input_refused' | 'command_input_unreachable' | 'command_input_line'} InputRefusalCode */
/** @typedef {{session_id: string, task: string, wait_id: string, line: string}} SessionInput */
/** @typedef {{typed: true}} TypedInput */
/**
 * Optional driver.input method. Atomically match the live session, task and wait_id
 * and claim that occurrence before typing. Resolve only after confirming delivery.
 * Reject with an InputRefusalCode in the error's code property for a definitive refusal. An uncertain delivery must
 * never make the occurrence available for a second write.
 * @callback InputHandler
 * @param {SessionInput} request
 * @returns {Promise<TypedInput>}
 */
