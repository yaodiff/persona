package node

// Built-in programs (#1830 steps 5 and 6, docs/plans/plan-extensions.md "Migrating cua / ego" and "How iMessage uses it"): the
// extensions cua (Cua Driver on this Mac), ego (ego lite) and imessage (this Mac's Messages, read-only) are programs on
// computers like any extension's, but they run in this process, not as a package the hub sends. A hub that sees them
// in node.info's builtin_programs asks them over plugin.node.call {id, method, params}; one from before asks by their
// old names (computer.<method>, browser.<method>, and for imessage connector.<method>), which stay. Both go through
// builtinCall: the same code answers either way.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/node/iphone"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// builtinPrograms are the built-in programs' ids (node.info's builtin_programs) and the prefix of their old method
// names that legacyBuiltinCall answers ("" for imessage: its old names, connector.*, have a case of their own in
// handle, and its methods keep those names over plugin.node.call, as the connector protocol has them; "" for iphone too:
// it came after builtin_programs, so a node that has it is never asked by an old name).
var builtinPrograms = map[string]string{"cua": "computer.", "ego": "browser.", "imessage": "", "iphone": ""}

// builtinProgramIDs is builtin_programs, in a fixed order.
var builtinProgramIDs = []string{"cua", "ego", "imessage", "iphone"}

// errNoBuiltinMethod is a method the built-in program does not have.
var errNoBuiltinMethod = errors.New("no such method")

// builtinCall answers method of the built-in program id (cua: status, call, install, grant, cancel, clipboard, cursor,
// nolock, lock; ego: status, install, open, cancel, call, cookies.list, cookies.import, cookies.clear; iphone: status,
// call, pair, forget, cancel).
// lcuCaller runs one computer.call on LCU, with the node's elicitations going to Persona's confirmation cards (lcu_mcp.go).
type lcuCaller func(ctx context.Context, params json.RawMessage) (*proto.ComputerResult, error)

func builtinCall(ctx context.Context, id, method string, params json.RawMessage, lcu lcuCaller) (any, error) {
	switch id {
	case "cua":
		backend := computerBackend(params) // the plugin's "Control backend": cua, lcu, or auto (lcu_status.go)
		switch method {
		case "status": // Cua Driver or LCU on this Mac, asked only while the user has it on (computer.go)
			return computerStatusFor(ctx, backend), nil
		case "call":
			if useLCU(backend) {
				if lcu == nil {
					return nil, errors.New("LCU 还没连上 hub，确认卡片发不出去")
				}
				return lcu(ctx, params)
			}
			return computerCall(ctx, params)
		case "install", "grant": // background jobs; their state comes back with status
			if backend == "lcu" && method == "grant" {
				return nil, fmt.Errorf("LCU 用 ChatGPT 应用自己的权限，第一次使用时在这台 Mac 上允许，不用单独授权")
			}
			run := installCua
			if method == "grant" {
				run = grantCua
			} else if backend == "lcu" {
				run = installLCU
			}
			if err := startJob(method, run); err != nil {
				return nil, err
			}
			return computerStatusFor(ctx, backend), nil
		case "cancel":
			cancelJob()
			return computerStatusFor(ctx, backend), nil
		case "clipboard": // the live screen's clipboard: text RFB cannot carry (Chinese), and reading it (clipboard.go)
			return macClipboard(ctx, params)
		case "cursor": // "Show the agent's cursor": Cua Driver's daemon with or without its cursor overlay (cua_extras.go)
			return setCursor(ctx, params)
		case "nolock": // "Don't auto-lock while the extension is on", said again by the hub every few minutes (awake.go)
			var p struct {
				On bool `json:"on"`
			}
			_ = json.Unmarshal(params, &p)
			noLock.set(p.On)
			return map[string]any{"held": noLock.held()}, nil
		case "lock": // "Lock" from the app, after a one-tap unlock (awake.go)
			if err := lockScreen(ctx); err != nil {
				return nil, err
			}
			return computerStatusFor(ctx, backend), nil
		}
	case "ego":
		switch method {
		case "status": // ego lite on this Mac, asked only while the user has the extension on (browser.go)
			return browserStatus(ctx), nil
		case "install", "open": // background jobs; their state comes back with status
			run := installEgo
			if method == "open" {
				run = openEgo
			}
			if err := egoJob.start(method, run, nil); err != nil {
				return nil, err
			}
			return browserStatus(ctx), nil
		case "cancel":
			egoJob.cancelInstall()
			return browserStatus(ctx), nil
		case "call":
			return browserCall(ctx, params)
		case "cookies.list", "cookies.import", "cookies.clear": // sign-ins in ego's profile (browser_cookies.go)
			return browserCookies(ctx, "browser."+method, params)
		}
	case "iphone":
		switch method {
		case "status": // the iPhones this computer reaches, asked only while the user has the extension on (iphone/)
			return iphone.Status(ctx), nil
		case "call":
			return iphone.Call(ctx, params)
		case "pair", "forget": // background jobs; their state comes back with status
			return iphone.Pair(ctx, method, params)
		case "cancel":
			iphone.Cancel()
			return iphone.Status(ctx), nil
		}
	case "imessage":
		switch method {
		case "connector.list", "connector.chats", "connector.older", "connector.changes", "connector.search": // connectors.go
			return localConnector(ctx, method, params)
		}
	}
	return nil, errNoBuiltinMethod
}

// legacyBuiltinCall answers a built-in program's method by its old name (computer.status, browser.call…), for a hub
// from before builtin_programs; a name it does not have is an unknown method, as it was.
func legacyBuiltinCall(ctx context.Context, method string, params json.RawMessage, lcu lcuCaller) (any, error) {
	for id, prefix := range builtinPrograms {
		if prefix == "" {
			continue
		}
		if short, ok := strings.CutPrefix(method, prefix); ok {
			out, err := builtinCall(ctx, id, short, params, lcu)
			if errors.Is(err, errNoBuiltinMethod) {
				return nil, fmt.Errorf("unknown method %s", method)
			}
			return out, err
		}
	}
	return nil, fmt.Errorf("unknown method %s", method)
}

// callBuiltin is plugin.node.call for a built-in program: its answer as the JSON a program's would be.
func callBuiltin(ctx context.Context, id, method string, params json.RawMessage, lcu lcuCaller) (json.RawMessage, error) {
	out, err := builtinCall(ctx, id, method, params, lcu)
	if errors.Is(err, errNoBuiltinMethod) {
		return nil, &rpc.Error{Code: extension.CodeUnknownMethod, Message: "unknown method " + method + " of the built-in program " + id}
	}
	if err != nil {
		return nil, err
	}
	return json.Marshal(out)
}
