package node

import (
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"runtime"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/preview"
)

// Native <select> bridge (docs/web/preview.md "Messages"): headless browsers render native <select> popups in a
// separate popup widget that never appears in Page.startScreencast frames. Once the web announces features {select:
// true}, this script runs in an isolated world (so page scripts never see the binding), intercepts opening single-choice
// <select> elements, and relays options and selections between the page and the web panel.
const (
	previewSelectBinding    = "__personaPreviewSelect"
	previewSelectWorld      = "__personaPreviewSelectWorld"
	previewSelectMaxOptions = 1000
	previewSelectMaxLabel   = 200
)

func previewSelectSource(isMac bool) string {
	macLit := "false"
	if isMac {
		macLit = "true"
	}
	return fmt.Sprintf(`((bindingName, isMac) => {
  if (globalThis.__personaPreviewSelectBridge) return;

  let seq = 0;
  let open = null;
  let suppressedClick = null;

  const isEligible = (el) => {
    if (!el || el.nodeType !== 1 || el.tagName !== "SELECT") return false;
    if (el.multiple || el.size > 1) return false;
    try {
      if (el.disabled || el.matches(":disabled")) return false;
    } catch (_) {
      if (el.disabled) return false;
    }
    return true;
  };

  const findSelect = (e) => {
    let target = e.target;
    if (typeof e.composedPath === "function") {
      const path = e.composedPath();
      if (path && path.length > 0) target = path[0];
    }
    if (!target || target.nodeType !== 1) return null;
    if (target.tagName === "SELECT") return target;
    return typeof target.closest === "function" ? target.closest("select") : null;
  };

  const collapse = (s) => {
    const t = String(s || "").replace(/\s+/g, " ").trim();
    const chars = Array.from(t);
    return chars.length > %d ? chars.slice(0, %d).join("") : t;
  };

  const isHidden = (el) => {
    if (!el) return true;
    if (el.hidden) return true;
    try {
      const cs = window.getComputedStyle(el);
      if (cs && cs.display === "none") return true;
    } catch (_) {}
    return false;
  };

  const topRect = (el) => {
    const r = el.getBoundingClientRect();
    let x = r.left;
    let y = r.top;
    let win = window;
    try {
      while (win !== win.top) {
        const fe = win.frameElement;
        if (!fe) return null;
        const fr = fe.getBoundingClientRect();
        const cs = win.parent.getComputedStyle(fe);
        x += fr.left + (parseFloat(cs.borderLeftWidth) || 0) + (parseFloat(cs.paddingLeft) || 0);
        y += fr.top + (parseFloat(cs.borderTopWidth) || 0) + (parseFloat(cs.paddingTop) || 0);
        win = win.parent;
      }
    } catch (_) {
      return null;
    }
    return { x, y, w: r.width, h: r.height };
  };

  const post = (msg) => {
    const fn = globalThis[bindingName];
    if (typeof fn === "function") {
      try { fn(JSON.stringify(msg)); } catch (_) {}
    }
  };

  const clearOpen = (notify) => {
    const cur = open;
    if (!cur) return;
    open = null;
    if (cur.observer) {
      try { cur.observer.disconnect(); } catch (_) {}
    }
    if (cur.onBlur) {
      try { cur.el.removeEventListener("blur", cur.onBlur, true); } catch (_) {}
    }
    if (notify) {
      post({ type: "close", token: cur.token });
    }
  };

  const openSelect = (sel) => {
    if (!isEligible(sel) || !sel.isConnected) return;
    const rect = topRect(sel);
    if (!rect) return;
    clearOpen(false);
    try {
      sel.focus({ preventScroll: true });
    } catch (_) {
      try { sel.focus(); } catch (_) {}
    }
    const token = ++seq;
    const options = [];
    const list = sel.options;
    for (let i = 0; i < list.length && options.length < %d; i++) {
      const opt = list[i];
      const grp = opt.parentElement && opt.parentElement.tagName === "OPTGROUP" ? opt.parentElement : null;
      if (isHidden(opt) || (grp && isHidden(grp))) continue;
      const item = { index: i, label: collapse(opt.label || opt.text || opt.textContent) };
      if (opt.disabled || (grp && grp.disabled)) item.disabled = true;
      if (grp) {
        const gl = collapse(grp.label);
        if (gl) item.group = gl;
      }
      options.push(item);
    }
    const onBlur = () => {
      if (open && open.token === token) clearOpen(true);
    };
    sel.addEventListener("blur", onBlur, true);
    const root = typeof sel.getRootNode === "function" ? sel.getRootNode() : document;
    const targetRoot = root && root.nodeType === 11 ? root : document;
    const observer = new MutationObserver(() => {
      if (!open || open.token !== token) return;
      if (!sel.isConnected || !isEligible(sel) || isHidden(sel)) {
        clearOpen(true);
      }
    });
    try {
      observer.observe(targetRoot, {
        subtree: true,
        childList: true,
        attributes: true,
        attributeFilter: ["disabled", "multiple", "size", "hidden", "style", "class"],
      });
    } catch (_) {}
    open = { token, el: sel, observer, onBlur };
    post({
      type: "open",
      token,
      rect,
      selected: sel.selectedIndex,
      options,
    });
  };

  window.addEventListener("mousedown", (e) => {
    if (e.defaultPrevented || e.button !== 0 || e.ctrlKey || e.metaKey || e.altKey) return;
    const sel = findSelect(e);
    if (!isEligible(sel)) return;
    e.preventDefault();
    suppressedClick = { el: sel, at: Date.now() };
    openSelect(sel);
  }, true);

  window.addEventListener("click", (e) => {
    const sel = findSelect(e);
    if (!isEligible(sel)) return;
    if (suppressedClick && suppressedClick.el === sel && Date.now() - suppressedClick.at < 1000) {
      suppressedClick = null;
      e.preventDefault();
      return;
    }
    if (!e.defaultPrevented) {
      e.preventDefault();
      openSelect(sel);
    }
  }, true);

  window.addEventListener("keydown", (e) => {
    if (e.defaultPrevented) return;
    const sel = findSelect(e);
    if (!isEligible(sel)) return;
    if (e.ctrlKey || e.metaKey || e.shiftKey) return;
    const k = e.key;
    let opens = false;
    if (!e.altKey) {
      if (k === " " || e.code === "Space" || k === "F4") {
        opens = true;
      } else if (isMac && (k === "ArrowDown" || k === "ArrowUp" || k === "Enter")) {
        opens = true;
      }
    } else if (k === "ArrowDown" || k === "ArrowUp") {
      opens = true;
    }
    if (!opens) return;
    e.preventDefault();
    if (e.repeat && open && open.el === sel) return;
    openSelect(sel);
  }, true);

  globalThis.__personaPreviewSelectBridge = {
    choose(token, index) {
      const cur = open;
      if (!cur || cur.token !== token) return false;
      const sel = cur.el;
      if (!sel || !sel.isConnected || !isEligible(sel)) {
        clearOpen(false);
        return false;
      }
      if (typeof index !== "number" || !Number.isInteger(index) || index < 0 || index >= sel.options.length) {
        return false;
      }
      const opt = sel.options[index];
      if (!opt) return false;
      const grp = opt.parentElement && opt.parentElement.tagName === "OPTGROUP" ? opt.parentElement : null;
      if (opt.disabled || (grp && grp.disabled) || isHidden(opt) || (grp && isHidden(grp))) {
        return false;
      }
      clearOpen(false);
      const prevIndex = sel.selectedIndex;
      const prevValue = sel.value;
      try {
        const root = typeof sel.getRootNode === "function" ? sel.getRootNode() : document;
        const active = root && "activeElement" in root ? root.activeElement : document.activeElement;
        if (active !== sel) sel.focus({ preventScroll: true });
      } catch (_) {}
      sel.selectedIndex = index;
      if (sel.selectedIndex !== prevIndex || sel.value !== prevValue) {
        sel.dispatchEvent(new Event("input", { bubbles: true, composed: true }));
        sel.dispatchEvent(new Event("change", { bubbles: true }));
      }
      return true;
    },
    close(token) {
      if (open && open.token === token) {
        clearOpen(false);
      }
    },
  };
})(%q, %s)`, previewSelectMaxLabel, previewSelectMaxLabel, previewSelectMaxOptions, previewSelectBinding, macLit)
}

// sendFrame writes one binary 'f' picture frame with CSS dimensions (w, h) and tracks it in inFlight until acked.
func (t *previewTab) sendFrame(w, h uint16, img []byte) {
	t.seq++
	msg := make([]byte, 9, 9+len(img))
	msg[0] = previewFrameMsg
	binary.BigEndian.PutUint32(msg[1:], t.seq)
	binary.BigEndian.PutUint16(msg[5:], w)
	binary.BigEndian.PutUint16(msg[7:], h)
	t.write(preview.Binary, append(msg, img...))
	if t.inFlight == nil {
		t.inFlight = map[uint32]struct{}{}
	}
	t.inFlight[t.seq] = struct{}{}
}

// ackFrame records that the web drew seq (screencast or still frame) and releases any held screencast acks once the
// in-flight window has room.
func (t *previewTab) ackFrame(seq uint32) {
	if seq != 0 {
		if _, ok := t.inFlight[seq]; !ok {
			return
		}
		delete(t.inFlight, seq)
	} else if len(t.inFlight) > 0 {
		var oldest uint32
		for s := range t.inFlight {
			if oldest == 0 || s < oldest {
				oldest = s
			}
		}
		delete(t.inFlight, oldest)
	}
	for len(t.heldAck) > 0 && len(t.inFlight) < previewInFlight {
		t.cdp("Page.screencastFrameAck", map[string]any{"sessionId": t.heldAck[0]})
		t.heldAck = t.heldAck[1:]
	}
	t.runPendingStill()
}

func (t *previewTab) runPendingStill() {
	if !t.stillInFlight && len(t.heldAck) == 0 && len(t.inFlight) < previewInFlight &&
		t.stillPendingGen != 0 && t.stillPendingGen == t.frameGen {
		gen := t.stillPendingGen
		t.stillPendingGen = 0
		t.captureStill(gen)
	}
}

func (t *previewTab) stopStill() {
	if t.stillTimer != nil {
		t.stillTimer.Stop()
		t.stillTimer = nil
	}
}

func (t *previewTab) resetStill() {
	t.stopStill()
	t.frameGen++
	t.stillPendingGen = 0
	t.stillSentPNG = false
	t.hasLastScreencast = false
}

func (t *previewTab) scheduleStill() {
	t.stopStill()
	gen := t.frameGen
	t.stillTimer = time.AfterFunc(previewStillDelay, func() {
		t.post(tabItem{fn: func() { t.captureStill(gen) }})
	})
}

func (t *previewTab) captureStill(gen uint64) {
	if gen == 0 || gen != t.frameGen || t.picking {
		return
	}
	if t.stillInFlight || len(t.inFlight) >= previewInFlight {
		t.stillPendingGen = gen
		return
	}
	vp := t.vp
	asPNG := t.featPNG
	params := map[string]any{"format": "jpeg", "quality": previewStillQuality}
	if asPNG {
		params = map[string]any{"format": "png"}
	}
	t.stillInFlight = true
	t.async("Page.captureScreenshot", params, func(raw json.RawMessage, err error) {
		t.stillInFlight = false
		if err != nil || gen != t.frameGen || vp != t.vp || (!asPNG && t.featPNG) {
			t.runPendingStill()
			return
		}
		var r struct {
			Data string `json:"data"`
		}
		if json.Unmarshal(raw, &r) != nil || r.Data == "" {
			t.runPendingStill()
			return
		}
		img, err := base64.StdEncoding.DecodeString(r.Data)
		if err != nil || len(img)+9 >= preview.MaxFrame {
			t.runPendingStill()
			return
		}
		if asPNG {
			t.stillSentPNG = true
		}
		t.sendFrame(uint16(vp.w), uint16(vp.h), img)
		t.runPendingStill()
	})
}

func (t *previewTab) applyFeatures(sel, png bool) {
	if png {
		wasPNG := t.featPNG
		t.featPNG = true
		if !wasPNG && t.hasLastScreencast && !t.stillSentPNG {
			if t.stillInFlight {
				t.stillPendingGen = t.frameGen
			} else {
				t.scheduleStill()
			}
		}
	}
	if sel && !t.featSelect {
		t.enableSelect()
	}
}

func (t *previewTab) enableSelect() {
	t.featSelect = true
	src := previewSelectSource(runtime.GOOS == "darwin")
	t.cdp("Runtime.addBinding", map[string]any{"name": previewSelectBinding, "executionContextName": previewSelectWorld})
	t.cdp("Page.addScriptToEvaluateOnNewDocument", map[string]any{"source": src, "worldName": previewSelectWorld})
	if t.frame == "" {
		return
	}
	t.selectActivating = true
	frameID := t.frame
	t.async("Page.createIsolatedWorld", map[string]any{"frameId": frameID, "worldName": previewSelectWorld}, func(raw json.RawMessage, err error) {
		var r struct {
			ExecutionContextID int `json:"executionContextId"`
		}
		if err != nil || json.Unmarshal(raw, &r) != nil || r.ExecutionContextID == 0 {
			t.finishSelectActivation()
			return
		}
		t.async("Runtime.evaluate", map[string]any{"contextId": r.ExecutionContextID, "expression": src}, func(_ json.RawMessage, _ error) {
			t.finishSelectActivation()
		})
	})
}

func (t *previewTab) finishSelectActivation() {
	t.selectActivating = false
	queued := t.deferredInput
	t.deferredInput = nil
	for _, in := range queued {
		t.fromWeb(in)
	}
}

type previewSelectRect struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
	W float64 `json:"w"`
	H float64 `json:"h"`
}

type previewSelectOption struct {
	Index    int    `json:"index"`
	Label    string `json:"label"`
	Disabled bool   `json:"disabled,omitempty"`
	Group    string `json:"group,omitempty"`
}

type previewSelectPayload struct {
	Type     string                `json:"type"`
	Token    uint32                `json:"token"`
	Rect     previewSelectRect     `json:"rect"`
	Selected int                   `json:"selected"`
	Options  []previewSelectOption `json:"options"`
}

func collapseSelectText(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) > previewSelectMaxLabel {
		return string(r[:previewSelectMaxLabel])
	}
	return s
}

func finiteRect(r previewSelectRect) bool {
	return !math.IsNaN(r.X) && !math.IsInf(r.X, 0) &&
		!math.IsNaN(r.Y) && !math.IsInf(r.Y, 0) &&
		!math.IsNaN(r.W) && !math.IsInf(r.W, 0) &&
		!math.IsNaN(r.H) && !math.IsInf(r.H, 0)
}

func (t *previewTab) onSelectBinding(execCtxID int, payload string) {
	if !t.featSelect {
		return
	}
	var msg previewSelectPayload
	if json.Unmarshal([]byte(payload), &msg) != nil || msg.Token == 0 {
		return
	}
	switch msg.Type {
	case "open":
		if !finiteRect(msg.Rect) {
			return
		}
		if len(msg.Options) > previewSelectMaxOptions {
			msg.Options = msg.Options[:previewSelectMaxOptions]
		}
		opts := make([]previewSelectOption, 0, len(msg.Options))
		valid := make(map[int]struct{}, len(msg.Options))
		for _, o := range msg.Options {
			if o.Index < 0 {
				continue
			}
			clean := previewSelectOption{
				Index:    o.Index,
				Label:    collapseSelectText(o.Label),
				Disabled: o.Disabled,
				Group:    collapseSelectText(o.Group),
			}
			opts = append(opts, clean)
			if !clean.Disabled {
				valid[clean.Index] = struct{}{}
			}
		}
		if msg.Selected < -1 {
			msg.Selected = -1
		}
		t.selectSeq++
		t.selectID = t.selectSeq
		t.selectToken = msg.Token
		t.selectExecCtxID = execCtxID
		t.selectValid = valid
		t.send(map[string]any{
			"t":        "select_open",
			"id":       t.selectID,
			"rect":     msg.Rect,
			"selected": msg.Selected,
			"options":  opts,
		})
	case "close":
		if t.selectID == 0 || msg.Token != t.selectToken || execCtxID != t.selectExecCtxID {
			return
		}
		t.withdrawSelect()
	}
}

func (t *previewTab) clearSelectState() {
	t.selectID = 0
	t.selectToken = 0
	t.selectExecCtxID = 0
	t.selectValid = nil
}

func (t *previewTab) withdrawSelect() {
	if t.selectID == 0 {
		return
	}
	id := t.selectID
	t.clearSelectState()
	t.send(map[string]any{"t": "select_close", "id": id})
}

func (t *previewTab) chooseSelect(id uint32, index *int) {
	if id == 0 || id != t.selectID || index == nil || *index < 0 {
		return
	}
	if _, ok := t.selectValid[*index]; !ok {
		return
	}
	token := t.selectToken
	ctxID := t.selectExecCtxID
	idx := *index
	t.clearSelectState()
	params := map[string]any{
		"expression": fmt.Sprintf("globalThis.__personaPreviewSelectBridge && globalThis.__personaPreviewSelectBridge.choose(%d, %d)", token, idx),
	}
	if ctxID != 0 {
		params["contextId"] = ctxID
	}
	t.cdp("Runtime.evaluate", params)
}

func (t *previewTab) closeSelectFromWeb(id uint32) {
	if id == 0 || id != t.selectID {
		return
	}
	token := t.selectToken
	ctxID := t.selectExecCtxID
	t.clearSelectState()
	params := map[string]any{
		"expression": fmt.Sprintf("globalThis.__personaPreviewSelectBridge && globalThis.__personaPreviewSelectBridge.close(%d)", token),
	}
	if ctxID != 0 {
		params["contextId"] = ctxID
	}
	t.cdp("Runtime.evaluate", params)
}
