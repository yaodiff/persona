package kblark

// The client's tests run lark-cli through the lark module's adapter, as the manager does (internal/manager links the
// built-in modules; the core reaches the adapter through internal/cliport).
import _ "github.com/nwverse/persona/server/internal/modules/all"
