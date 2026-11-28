package assistant

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"unicode/utf16"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/lazyre"
)

const webSansKey = "web.fonts.sans"
const webMonoKey = "web.fonts.mono"

type webFontChoice struct {
	Preset string `json:"preset"`
	Family string `json:"family,omitempty"`
}

type webFonts struct {
	Sans webFontChoice `json:"sans"`
	Mono webFontChoice `json:"mono"`
}

var webFontPreset = lazyre.New(`^[a-z][a-zA-Z0-9]{0,31}$`)

// normalize accepts future preset identifiers and a single CSS-quoted family for Custom.
func (c *webFontChoice) normalize() bool {
	if !webFontPreset.MatchString(c.Preset) {
		return false
	}
	if c.Preset != "custom" {
		return c.Family == ""
	}
	if strings.ContainsAny(c.Family, ",;{}<>") || strings.ContainsFunc(c.Family, func(r rune) bool { return r < 32 || r == 127 }) {
		return false
	}
	c.Family = strings.TrimSpace(c.Family)
	return c.Family != "" && len(utf16.Encode([]rune(c.Family))) <= 100
}

// readWebFonts uses separate settings so updates to the two font choices do not overwrite one another.
func (s *Service) readWebFonts(ctx context.Context) (webFonts, error) {
	out := webFonts{Sans: webFontChoice{Preset: "default"}, Mono: webFontChoice{Preset: "default"}}
	st := s.store()
	if st == nil {
		return out, errors.New("web font settings need a database")
	}
	if _, err := st.Setting(ctx, webSansKey, &out.Sans); err != nil {
		return out, err
	}
	_, err := st.Setting(ctx, webMonoKey, &out.Mono)
	return out, err
}

func (s *Service) webFontRoutes() {
	read := func(w http.ResponseWriter, r *http.Request) {
		out, err := s.readWebFonts(r.Context())
		if err != nil {
			apierr.Write(w, http.StatusServiceUnavailable, err)
			return
		}
		apierr.WriteJSON(w, out)
	}
	s.handle("GET /api/v1/settings/web-fonts", read)
	s.handle("PUT /api/v1/settings/web-fonts", func(w http.ResponseWriter, r *http.Request) {
		if !fullRole(r) {
			apierr.Write(w, http.StatusForbidden, errors.New("only full access may change web font settings"))
			return
		}
		var in struct {
			Sans *webFontChoice `json:"sans"`
			Mono *webFontChoice `json:"mono"`
		}
		err := decodeBody(w, r, &in, 4<<10)
		if err != nil || (in.Sans == nil) == (in.Mono == nil) {
			writeBodyErr(w, err, errors.New("send one font choice: sans or mono"))
			return
		}
		key, choice := webSansKey, in.Sans
		if choice == nil {
			key, choice = webMonoKey, in.Mono
		}
		if !choice.normalize() {
			apierr.Write(w, http.StatusBadRequest, errors.New("invalid web font choice"))
			return
		}
		st := s.store()
		if st == nil {
			apierr.Write(w, http.StatusServiceUnavailable, errors.New("web font settings need a database"))
			return
		}
		if err := st.PutSetting(r.Context(), key, *choice); err != nil {
			apierr.Write(w, http.StatusServiceUnavailable, err)
			return
		}
		read(w, r)
	})
}
