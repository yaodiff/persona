// The core's C entry points (docs/plans/plan-rust-core.md, "The boundary: a few entry points and bytes").
// Every message is a bincode-encoded wire type; the generated codecs read and write them.
#ifndef PERSONA_CORE_H
#define PERSONA_CORE_H

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

typedef struct PersonaCore PersonaCore;

// The client's callbacks. `changed` and `run` are called on the core's worker thread; `release` once, when the core
// is freed. `context` is passed back unchanged and must be safe to use from that thread. Its ownership transfers to
// the core on every persona_core_new call, success or failure; release drops it exactly once. Callback pointers are
// borrowed only for the call and must be copied before returning. Callbacks must not synchronously flush or free the
// same core: the worker cannot join or wait for itself. A handle has one owner and one free. Calls may run concurrently
// with that free: each either leases the live runtime or fails closed. Free removes new leases, requests worker
// shutdown and joins it outside the registry lock. An ordinary call never joins the worker on its final lease; it may
// finish after close starts. Close is an orderly shutdown and cannot be called synchronously from its own callback.
// Handles are never dereferenced by the implementation and are not reused during this process.
typedef struct PersonaCoreHost {
    void *context;
    void (*changed)(void *context, uint64_t version);
    void (*run)(void *context, const uint8_t *effect, size_t length);
    void (*release)(void *context);
} PersonaCoreHost;

// The legacy four-field host layout stays stable. Optional targeted wake is registered separately.
typedef void (*PersonaCoreChatCallback)(void *context, const uint8_t *chat, size_t length, uint64_t version);

// Bytes the core allocated; give them back with persona_core_bytes_free.
typedef struct PersonaCoreBytes {
    uint8_t *data;
    size_t length;
    size_t capacity;
} PersonaCoreBytes;

// Starts a core from an encoded Config. NULL when the config does not decode.
PersonaCore *persona_core_new(PersonaCoreHost host, const uint8_t *config, size_t length);
// Optionally register a chat-targeted wake. Until registered, `changed` remains the version-only callback.
// The callback runs on the worker and must return promptly; it is removed when the core is closed.
bool persona_core_set_changed_chat(PersonaCore *core, PersonaCoreChatCallback callback);
// Queues an encoded Action. Safe on the main thread: it only copies the bytes.
bool persona_core_dispatch(PersonaCore *core, const uint8_t *action, size_t length);
// The chat's encoded ChatPatch since the last drain. `chat` is UTF-8.
PersonaCoreBytes persona_core_drain(PersonaCore *core, const uint8_t *chat, size_t length);
void persona_core_bytes_free(PersonaCoreBytes bytes);
// Waits until queued actions are applied. For tests; never on a main thread. Zero checks once without waiting.
bool persona_core_flush(PersonaCore *core, uint32_t timeout_ms);
// Removes new runtime leases, stops and joins the worker outside the registry lock, then releases the host.
// Ordinary dispatch/drain calls never perform this worker join on their final lease.
void persona_core_free(PersonaCore *core);

// Projects the Agents list from a UTF-8 JSON input (sort::Input). Returns a UTF-8 JSON projection (sort::Projection).
// Pure function: no core handle, no dispatch, no drain. Free the result with persona_core_bytes_free.
PersonaCoreBytes persona_core_project_sort(const uint8_t *input, size_t length);

#ifdef __cplusplus
}
#endif

#endif
