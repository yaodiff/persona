package extmgr

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/nwverse/persona/sdk/go/wire"
	"github.com/nwverse/persona/server/internal/extension"
)

func installedIconURL(id, version string) string {
	return "/api/v1/extensions/" + url.PathEscape(id) + "/-/icon?v=" + url.QueryEscape(version)
}

func catalogIconURL(mid string, e *extension.MarketEntry) string {
	if !extension.ValidIconPath(e.Icon) {
		return e.IconURL
	}
	pkg := e.PackageFor(extension.Platform())
	if pkg == nil || e.Parsed == nil {
		return ""
	}
	return "/api/v1/extensions/-/catalog/" + url.PathEscape(mid) + "/" + url.PathEscape(e.Parsed.ID) + "/icon?sha256=" + url.QueryEscape(pkg.SHA256)
}

func (h *Manager) extensionLogoRoutes(api *http.ServeMux) {
	allow := func(w http.ResponseWriter, r *http.Request) bool {
		p := principalOf(r.Context())
		if p.Role != roleFull || p.Extension != "" {
			writeErr(w, http.StatusForbidden, errors.New("this device can only read and answer"))
			return false
		}
		return true
	}
	missing := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		http.NotFound(w, r)
	}
	api.HandleFunc("GET /api/v1/extensions/{id}/-/icon", func(w http.ResponseWriter, r *http.Request) {
		if !allow(w, r) {
			return
		}
		p := h.extensions.reg.GetExact(r.PathValue("id"))
		if p == nil || p.Dir == "" || r.URL.Query().Get("v") != p.Record.Version {
			missing(w, r)
			return
		}
		release, err := h.images.slot(r.Context(), "-installed-logos")
		if err != nil {
			missing(w, r)
			return
		}
		defer release()
		data, err := extension.ReadIcon(p.Dir)
		if cur := h.extensions.reg.GetExact(r.PathValue("id")); err != nil || cur != p {
			missing(w, r)
			return
		}
		serveLogo(w, r, data)
	})
	api.HandleFunc("GET /api/v1/extensions/-/catalog/{mid}/{id}/icon", func(w http.ResponseWriter, r *http.Request) {
		if !allow(w, r) {
			return
		}
		if h.marketsLoad(r.Context()) != nil {
			missing(w, r)
			return
		}
		mid, id := r.PathValue("mid"), r.PathValue("id")
		rec, entry, err := h.marketEntry(mid, id)
		if err != nil || entry.Parsed.ID != id || !extension.ValidIconPath(entry.Icon) {
			missing(w, r)
			return
		}
		pkg := entry.PackageFor(extension.Platform())
		if pkg == nil || r.URL.Query().Get("sha256") != pkg.SHA256 {
			missing(w, r)
			return
		}
		// Copy mutable public fields before any I/O; no market lock spans a download.
		selected := *pkg
		data, err := h.catalogLogo(r.Context(), rec, id, selected)
		current, currentEntry, currentErr := h.marketEntry(mid, id)
		if err != nil || currentErr != nil || !sameMarketTrust(current, rec) || currentEntry.Parsed.ID != id || currentEntry.Icon != entry.Icon {
			missing(w, r)
			return
		}
		currentPackage := currentEntry.PackageFor(extension.Platform())
		if currentPackage == nil || currentPackage.URL != selected.URL || currentPackage.SHA256 != selected.SHA256 {
			missing(w, r)
			return
		}
		serveLogo(w, r, data)
	})
}

func serveLogo(w http.ResponseWriter, r *http.Request, data []byte) {
	sum := sha256.Sum256(data)
	etag := `"` + hex.EncodeToString(sum[:]) + `"`
	hd := w.Header()
	hd.Set("Content-Type", "image/png")
	hd.Set("Cache-Control", "private, max-age=300")
	hd.Set("ETag", etag)
	hd.Set("X-Content-Type-Options", "nosniff")
	hd.Set("Content-Security-Policy", "default-src 'none'")
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	hd.Set("Content-Length", strconv.Itoa(len(data)))
	_, _ = w.Write(data)
}

// catalogLogo reuses the image proxy's bounded cache, slots, and single-flight map.
// Package downloads retain their existing authentication, digest, and shared-cache checks.
func (h *Manager) catalogLogo(ctx context.Context, rec extension.MarketRecord, id string, pkg extension.MarketPackage) ([]byte, error) {
	px := &h.images
	dir := h.imagesDir()
	file := imageFile(dir, "-catalog-logos", id, rec.Spec+"\x00"+pkg.SHA256, 0)
	if img, _, ok := cachedImage(file, px.clock()); ok && img.Mime == "image/png" && extension.CheckIcon(img.Data) == nil {
		return img.Data, nil
	}
	key := "catalog-logo\x00" + file
	px.mu.Lock()
	if f := px.inflight[key]; f != nil {
		px.mu.Unlock()
		select {
		case <-f.done:
			if f.img == nil {
				return nil, f.err
			}
			return f.img.Data, f.err
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if px.inflight == nil {
		px.inflight = map[string]*imageFetch{}
	}
	f := &imageFetch{done: make(chan struct{})}
	px.inflight[key] = f
	px.mu.Unlock()
	defer func() {
		px.mu.Lock()
		delete(px.inflight, key)
		px.mu.Unlock()
		close(f.done)
	}()
	cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Minute)
	defer cancel()
	release, err := px.slot(cctx, "-catalog-logos")
	if err != nil {
		f.err = err
		return nil, err
	}
	defer release()
	mk, err := h.market(rec)
	if err != nil {
		f.err = err
		return nil, err
	}
	stage, err := os.MkdirTemp("", "persona-extension-logo-")
	if err != nil {
		f.err = err
		return nil, err
	}
	defer os.RemoveAll(stage)
	archive, name, err := mk.Download(cctx, &pkg, stage)
	if err == nil {
		var data []byte
		data, err = extension.PackageIcon(archive, name, id)
		if err == nil {
			f.img, f.exp = &wire.CheckedImage{Data: data, Mime: "image/png"}, px.clock().Add(24*time.Hour)
			px.keep(dir, file, f.img, f.exp)
			return data, nil
		}
	}
	f.err = err
	return nil, err
}

func (h *Manager) decorateInstalledLogo(v *ExtensionView, p *extension.Extension) {
	if extension.HasIcon(p.Dir) {
		v.IconURL = installedIconURL(p.Record.ID, p.Record.Version)
	} else if strings.HasPrefix(v.IconURL, "/api/v1/extensions/") {
		v.IconURL = ""
	}
}
