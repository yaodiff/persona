package extmgr

// The assistant driving a browser on the user's Macs through ego lite: the built-in plugin ego, off until the user
// turns it on, then Mac by Mac (docs/plans/plan-ego-browser.md). While it is off no node is asked about ego, the assistant
// has no browser_* tools and the machines carry no Browser. Like the plugin cua (computer.go), whose settings, switch
// and tool results it shares.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

const egoExtensionID = "ego"

const egoConfig = `{
  "type": "object",
  "properties": {
    "machines": {"type": "array", "title": "能用的浏览器", "description": "打开的 Mac 上，助手才能用 ego lite。开关即时生效，和它机器页上的「允许助手用这台的浏览器」是同一个", "items": {"type": "string", "format": "machine", "x-os": ["darwin"]}, "uniqueItems": true},
    "confirm": {"type": "boolean", "title": "操作前在手机上确认", "description": "打开后，助手每次在浏览器里跑脚本（打开网页、点按、填表）之前先在手机上问你；关着时直接做，记进 hub 的日志", "default": false}
  }
}`

func egoManifest(h *Manager) *extension.Manifest {
	items, _ := json.Marshal([]extension.ComputerItem{{ID: egoExtensionID, Name: "ego lite"}})
	return &extension.Manifest{ManifestVersion: extension.ManifestVersion, ID: egoExtensionID, Name: "浏览器（ego lite）", Version: h.version,
		Description: "让助手在你的 Mac 上用 ego lite 打开网页、点按、填表、截图，用的是你在 ego 里登录过的网站。要在那台 Mac 上装好 ego lite 并做完它的首次设置。",
		Homepage:    "https://lite.ego.app", Contributes: map[string]json.RawMessage{"browser": items},
		Configuration: json.RawMessage(egoConfig), Node: builtinNode("machines")}
}

// browserOf is m's proto.Machine.Browser: nil unless the plugin is on and m is a Mac; else the last status its node
// gave (every Mac's while the plugin is on), with Enabled when the plugin is on for m.
func (h *Manager) browserOf(m *Machine) *proto.Browser {
	s := h.machineExtension(egoExtensionID)
	m.mu.Lock()
	darwin, last := m.info.OS == "darwin", m.browser
	m.mu.Unlock()
	if !s.On || !darwin {
		return nil
	}
	b := proto.Browser{}
	if last != nil {
		b = *last
	}
	b.Enabled = slices.Contains(s.Machines, m.cfg.ID)
	return &b
}

// refreshBrowser asks m's node what it has of ego lite now, while the plugin is on and m is online; the machine list
// goes out again when that changed.
func (h *Manager) refreshBrowser(ctx context.Context, m *Machine) *proto.Browser {
	if b := h.browserOf(m); b == nil || !m.isOnline() {
		return b
	}
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	asked := time.Now()
	var b proto.Browser
	if err := m.callBuiltin(ctx, "browser.status", map[string]any{}, &b); err != nil {
		if !strings.Contains(err.Error(), "unknown method") {
			return h.browserOf(m)
		}
		b = proto.Browser{Note: i18n.T(i18n.Default, "hub-browser.note_outdated")}
	}
	if h.keepBrowser(m, b, asked) {
		h.broadcastMachines()
	}
	return h.browserOf(m)
}

// keepBrowser stores what m's node answered unless what is kept is newer (by the node's checked; else when asked),
// as keepComputer does; true when what is kept changed.
func (h *Manager) keepBrowser(m *Machine, b proto.Browser, asked time.Time) bool {
	when := b.Checked
	if when.IsZero() {
		when = asked
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if when.Before(m.browserAt) {
		return false
	}
	old := m.browser
	m.browser, m.browserAt = &b, when
	return old == nil || !sameBrowser(*old, b)
}

// sameBrowser: the same apart from when it was checked; the job and the displays by value.
func sameBrowser(a, b proto.Browser) bool {
	a.Checked, b.Checked = time.Time{}, time.Time{}
	return reflect.DeepEqual(a, b)
}

// browserChanged: the plugin was turned on or off, or its machines changed: ask the Macs again.
func (h *Manager) browserChanged(ctx context.Context) {
	for _, m := range h.machinesNow() {
		if b := h.browserOf(m); b != nil && m.isOnline() {
			go h.refreshBrowser(ctx, m)
		}
	}
	h.broadcastMachines()
}

// getBrowser: GET /api/v1/machines/{mid}/browser, what the Mac has of ego lite now (the machine page asks when it
// opens, and again while its setup goes on). 404 while the plugin is off or it is not a Mac.
func (h *Manager) getBrowser(w http.ResponseWriter, r *http.Request, m *Machine) {
	b := h.refreshBrowser(r.Context(), m)
	if b == nil {
		writeErr(w, http.StatusNotFound, i18n.Err("hub-browser.err_off"))
		return
	}
	writeJSON(w, i18n.BrowserStatus(h.requestLang(r), b))
}

// putBrowser: PUT /api/v1/machines/{mid}/browser {"enabled": bool}, the machine page's switch (the plugin's machines).
func (h *Manager) putBrowser(w http.ResponseWriter, r *http.Request, m *Machine) {
	var in struct {
		Enabled bool `json:"enabled"`
	}
	if err := decodeJSONBody(w, r, &in, 4<<10); err != nil {
		h.writeComputerBodyErr(w, r, err)
		return
	}
	if h.browserOf(m) == nil {
		writeErr(w, http.StatusNotFound, i18n.Err("hub-browser.err_off"))
		return
	}
	if err := h.setExtensionMachine(r.Context(), egoExtensionID, m.cfg.ID, in.Enabled); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, i18n.BrowserStatus(h.requestLang(r), h.refreshBrowser(r.Context(), m)))
}

// postBrowserJob: POST /api/v1/machines/{mid}/browser/{install|open|cancel}, the machine page's "Install ego lite", "Open
// ego lite" and "Cancel"; the node runs it in the background, its state comes back as Browser.job.
func (h *Manager) postBrowserJob(w http.ResponseWriter, r *http.Request, m *Machine) {
	action := r.PathValue("action")
	if action != "install" && action != "open" && action != "cancel" {
		writeErr(w, http.StatusNotFound, i18n.ErrEnglish("hub-computer.err_unknown_action"))
		return
	}
	if b := h.browserOf(m); b == nil || !b.Enabled {
		writeErr(w, http.StatusNotFound, i18n.Err("hub-browser.err_not_enabled"))
		return
	}
	if !m.isOnline() {
		writeErr(w, http.StatusConflict, i18n.Err("hub-browser.err_offline"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	asked := time.Now()
	var b proto.Browser
	if err := m.callBuiltin(ctx, "browser."+action, map[string]any{}, &b); err != nil {
		if strings.Contains(err.Error(), "unknown method") {
			err = i18n.Err("hub-browser.err_outdated_install")
		}
		h.writeComputerErr(w, r, http.StatusConflict, err)
		return
	}
	h.logf("[%s] browser %s", m.cfg.ID, action)
	if h.keepBrowser(m, b, asked) {
		h.broadcastMachines()
	}
	writeJSON(w, i18n.BrowserStatus(h.requestLang(r), h.browserOf(m)))
}

// ---- the assistant's tools ----
// Tool diagnostics below are model instructions and raw runtime output, not client interface copy.

func (h *Manager) browserTools() []proto.Tool {
	if s := h.machineExtension(egoExtensionID); !s.On || len(s.Machines) == 0 {
		return nil
	}
	return []proto.Tool{mcpTool("browser_run"), mcpTool("browser_look"), mcpTool("browser_fetch_download")}
}

func isBrowserTool(name string) bool {
	return name == "browser_run" || name == "browser_look" || name == "browser_fetch_download"
}

// Browser scripts and snapshots share the conversation's task space in ego.
func (h *Manager) callBrowserTool(ctx context.Context, callerMachine, sessionID, name string, args json.RawMessage) proto.ToolResult {
	if name == "browser_fetch_download" {
		return h.fetchBrowserDownload(ctx, callerMachine, sessionID, args)
	}
	var a struct {
		Machine   string `json:"machine"`
		Script    string `json:"script"`
		Full      bool   `json:"full"`
		TimeoutMS int    `json:"timeout_ms"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return proto.TextResult("bad arguments: "+err.Error(), true)
	}
	m, err := h.browserMachine(a.Machine)
	if err != nil {
		return proto.TextResult(err.Error(), true)
	}
	in := map[string]any{"session": "persona-" + convID(sessionID)}
	if name == "browser_look" {
		in["look"], in["full"] = true, a.Full
	} else {
		if strings.TrimSpace(a.Script) == "" {
			return proto.TextResult("give script: the body of an async function, with task and page (tab p1) at hand", true)
		}
		in["script"], in["screenshot"], in["timeout_ms"] = a.Script, true, a.TimeoutMS
		first, _, _ := strings.Cut(strings.TrimSpace(a.Script), "\n")
		if h.machineExtension(egoExtensionID).Confirm {
			if ok, outcome := h.approveOutcome(ctx, h.assistantAsker(sessionID, m.cfg.ID), name, i18n.T(h.userLang(), "hub-browser.confirm_script", i18n.Args{"machine": m.name(), "line": first}), args); !ok {
				h.logf("[%s] browser_run %s (%d bytes)", m.cfg.ID, refusalWord(outcome), len(a.Script))
				return notApproved(outcome)
			}
		}
	}
	start := time.Now()
	var res proto.ComputerResult
	callErr := m.callBuiltin(ctx, "browser.call", in, &res)
	h.logf("[%s] %s for %s: %d bytes, %v, %s", m.cfg.ID, name, convID(sessionID), len(a.Script), time.Since(start).Round(time.Millisecond),
		map[bool]string{true: "failed", false: "ok"}[callErr != nil])
	if callErr != nil {
		return proto.TextResult(callErr.Error(), true)
	}
	return browserResult(&res)
}

func (h *Manager) fetchBrowserDownload(ctx context.Context, callerMachine, sessionID string, args json.RawMessage) proto.ToolResult {
	var a struct {
		Machine  string `json:"machine"`
		Filename string `json:"filename"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return proto.TextResult("bad arguments: "+err.Error(), true)
	}
	if strings.TrimSpace(a.Machine) == "" {
		return proto.TextResult("give the name or id of the browser machine", true)
	}
	if a.Filename == "" || a.Filename == "." || a.Filename == ".." ||
		strings.ContainsAny(a.Filename, "/\\\x00") || filepath.Base(a.Filename) != a.Filename {
		return proto.TextResult("give the exact filename in Persona browser downloads, without a directory", true)
	}
	src, err := h.browserMachine(a.Machine)
	if err != nil {
		return proto.TextResult(err.Error(), true)
	}
	dst := src
	if callerMachine != "" && callerMachine != src.cfg.ID {
		dst = h.machineByName(callerMachine)
		if dst == nil {
			return proto.TextResult(fmt.Sprintf("no caller machine %q for the download", callerMachine), true)
		}
		if !dst.isOnline() {
			return proto.TextResult(fmt.Sprintf("the caller's machine %s is offline", dst.cfg.ID), true)
		}
	}
	for _, m := range []*Machine{src, dst} {
		if m.config().trust == "work" {
			return proto.TextResult(fmt.Sprintf("%s is a work computer. Browser downloads are only available on personal computers", m.name()), true)
		}
	}
	if h.machineExtension(egoExtensionID).Confirm {
		summary := i18n.T(h.userLang(), "hub-browser.confirm_download", i18n.Args{
			"filename": a.Filename, "source": src.name(), "destination": dst.name(),
		})
		if ok, outcome := h.approveOutcome(ctx, h.assistantAsker(sessionID, src.cfg.ID), "browser_fetch_download", summary, args); !ok {
			return notApproved(outcome)
		}
	}
	var info proto.BrowserDownloadInfo
	if err := src.callBuiltin(ctx, "browser.download_info", map[string]string{"filename": a.Filename}, &info); err != nil {
		return proto.TextResult(fmt.Sprintf("read download info from %s: %v", src.name(), err), true)
	}
	if info.Name != a.Filename || info.Path == "" || info.Size < 0 || info.Size > proto.MaxTerminalUploadBytes ||
		len(info.SHA256) != sha256.Size*2 {
		return proto.TextResult("the browser machine returned invalid download metadata", true)
	}
	if _, err := hex.DecodeString(info.SHA256); err != nil {
		return proto.TextResult("the browser machine returned an invalid download SHA-256", true)
	}
	path, copied := info.Path, src.cfg.ID != dst.cfg.ID
	if copied {
		path, err = h.copyBrowserDownload(ctx, src, dst, info)
		if err != nil {
			return proto.TextResult(err.Error(), true)
		}
	}
	receipt, _ := json.Marshal(map[string]any{
		"source_machine": src.cfg.ID, "dest_machine": dst.cfg.ID, "filename": info.Name, "path": path,
		"size": info.Size, "sha256": info.SHA256, "copied": copied,
	})
	return proto.TextResult(string(receipt), false)
}

func (h *Manager) copyBrowserDownload(ctx context.Context, src, dst *Machine, info proto.BrowserDownloadInfo) (string, error) {
	var begin struct {
		UploadID   string `json:"upload_id"`
		ChunkBytes int    `json:"chunk_bytes"`
		SHA256     bool   `json:"sha256"`
	}
	if err := dst.call(ctx, "file.upload.begin", map[string]any{
		"name": info.Name, "size": info.Size, "dest": proto.UploadDestTerminal, "sha256": info.SHA256,
	}, &begin); err != nil {
		return "", fmt.Errorf("start download upload on %s: %w", dst.name(), err)
	}
	if begin.UploadID == "" {
		return "", fmt.Errorf("the caller's machine %s did not return an upload ID", dst.name())
	}
	complete := false
	defer func() {
		if complete {
			return
		}
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		if err := dst.call(cleanup, "file.upload.abort", map[string]string{"upload_id": begin.UploadID}, nil); err != nil {
			h.logf("[%s] browser_fetch_download: could not abort the upload: %v", dst.cfg.ID, err)
		}
	}()
	if !begin.SHA256 {
		return "", fmt.Errorf("update Persona Link on %s to copy downloads with SHA-256 verification", dst.name())
	}

	chunkBytes := min(proto.FileDownloadChunkBytes, proto.UploadChunkBytes)
	if begin.ChunkBytes > 0 {
		chunkBytes = min(chunkBytes, begin.ChunkBytes)
	}
	digest := sha256.New()
	var offset int64
	for offset < info.Size {
		length := min(int64(chunkBytes), info.Size-offset)
		var chunk proto.BrowserDownloadChunk
		if err := src.callBuiltin(ctx, "browser.download_chunk", map[string]any{
			"filename": info.Name, "offset": offset, "length": length,
		}, &chunk); err != nil {
			return "", fmt.Errorf("read download at byte %d from %s: %w", offset, src.name(), err)
		}
		if chunk.Name != info.Name || chunk.Offset != offset || chunk.Size != info.Size ||
			int64(len(chunk.Data)) != length || chunk.EOF != (offset+length == info.Size) {
			return "", fmt.Errorf("invalid download chunk at byte %d from %s", offset, src.name())
		}
		if err := dst.call(ctx, "file.upload.chunk", map[string]any{
			"upload_id": begin.UploadID, "offset": offset, "data": chunk.Data,
		}, nil); err != nil {
			return "", fmt.Errorf("upload download at byte %d to %s: %w", offset, dst.name(), err)
		}
		digest.Write(chunk.Data)
		offset += int64(len(chunk.Data))
	}
	if offset != info.Size || !strings.EqualFold(hex.EncodeToString(digest.Sum(nil)), info.SHA256) {
		return "", fmt.Errorf("download changed during the transfer: size or SHA-256 mismatch")
	}
	var uploaded proto.UploadedFile
	if err := dst.call(ctx, "file.upload.end", map[string]string{
		"upload_id": begin.UploadID, "sha256": info.SHA256,
	}, &uploaded); err != nil {
		return "", fmt.Errorf("finish download upload on %s: %w", dst.name(), err)
	}
	if uploaded.Path == "" || uploaded.Size != info.Size || !strings.EqualFold(uploaded.SHA256, info.SHA256) {
		return "", fmt.Errorf("%s did not confirm the download's path, size and SHA-256. Check the destination before retrying", dst.name())
	}
	complete = true
	return uploaded.Path, nil
}

// browserResult preserves a script's error as a failed tool call. The node returns it in JSON alongside page metadata
// and an optional screenshot so the model can diagnose the page; transport success does not mean the script succeeded.
func browserResult(r *proto.ComputerResult) proto.ToolResult {
	out := computerResult(r)
	var result struct {
		Error string `json:"error"`
	}
	if len(r.Output) > 0 && json.Unmarshal(r.Output, &result) == nil && strings.TrimSpace(result.Error) != "" {
		out.IsError = true
	}
	return out
}

// browserMachine is the Mac the assistant named, when the plugin is on for it and it is online.
func (h *Manager) browserMachine(name string) (*Machine, error) {
	m := h.machineByName(strings.TrimSpace(name))
	ids := strings.Join(h.machineExtension(egoExtensionID).Machines, ", ")
	if ids == "" {
		ids = "none"
	}
	if m == nil {
		return nil, fmt.Errorf("no machine %q (the browser is on for: %s)", name, ids)
	}
	if b := h.browserOf(m); b == nil || !b.Enabled {
		return nil, fmt.Errorf("the browser on %s is not turned on (on for: %s); the user turns it on on the machine's page in the app", m.cfg.ID, ids)
	}
	if !m.isOnline() {
		return nil, fmt.Errorf("%s is offline", m.cfg.ID)
	}
	return m, nil
}
