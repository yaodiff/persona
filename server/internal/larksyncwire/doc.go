// Package larksyncwire is the contract between the extension manager (internal/extmgr) and the Lark module's user
// sync (internal/modules/lark/larksync): the owner authorizes a bot's app to read as them once, and every day the
// sync reads what changed in their chats, drive, documents, wiki and minutes into the knowledge base
// (docs/extensions/bots.md "Reading as you: daily sync").
//
// The module owns the open platform, the sync state, routes, scheduler and connector; the manager keeps the settings
// and Postgres storage behind the seam (internal/botport). Types, constants and trivial pure helpers only
// (docs/server/architecture.md "Contract packages").
//
//persona:layer contract
package larksyncwire
