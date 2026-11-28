package wire

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"image/png"
	"io"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Looks' files (docs/extensions/extensions.md "Avatars"): what the hub holds a look's pictures to when it installs an extension and
// before it serves them, here so an extension's own tests can hold them to the same (CheckLooks).

const (
	LookMaxFile    = 4 << 20   // a PNG
	LookMaxSVG     = 256 << 10 // an SVG
	LookMaxTotal   = 16 << 20  // a look
	LookMinSide    = 16
	lookMaxSide    = 1024 // an avatar's side
	lookMaxDotSide = 512  // a dot frame's side
)

// CheckLookPNG holds a picture to being a square PNG of side px (0: any) at most most px wide.
func CheckLookPNG(b []byte, side, most int) error {
	cfg, err := png.DecodeConfig(bytes.NewReader(b))
	if err != nil {
		return errors.New("not a PNG")
	}
	if cfg.Width != cfg.Height {
		return fmt.Errorf("%d×%d, not square", cfg.Width, cfg.Height)
	}
	if cfg.Width > most || cfg.Width < LookMinSide {
		return fmt.Errorf("%d px, %d to %d", cfg.Width, LookMinSide, most)
	}
	if side > 0 && cfg.Width != side {
		return fmt.Errorf("%d px, its key says %d", cfg.Width, side)
	}
	return nil
}

// ReadLookFile reads a file a look names: a regular file inside the package at root (a link may not lead out of it)
// within LookMaxFile (LookMaxSVG for an SVG).
func ReadLookFile(root, name string) ([]byte, error) {
	clean := path.Clean(name)
	if name == "" || clean != name || path.IsAbs(name) || strings.Contains(name, `\`) || clean == ".." || strings.HasPrefix(clean, "../") {
		return nil, fmt.Errorf("%q is not a path inside the package", name)
	}
	p := filepath.Join(root, filepath.FromSlash(name))
	resolved, err := filepath.EvalSymlinks(p)
	if err != nil {
		return nil, fmt.Errorf("%s 不在包里", name)
	}
	rootResolved, _ := filepath.EvalSymlinks(root)
	if rel, err := filepath.Rel(rootResolved, resolved); err != nil || rel == ".." || strings.HasPrefix(filepath.ToSlash(rel), "../") {
		return nil, fmt.Errorf("%s 跳出了包", name)
	}
	fi, err := os.Stat(resolved)
	if err != nil || !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("%s 不是普通文件", name)
	}
	most := int64(LookMaxFile)
	if strings.HasSuffix(name, ".svg") {
		most = LookMaxSVG
	}
	if fi.Size() > most {
		return nil, fmt.Errorf("%s: %d bytes, more than %d", name, fi.Size(), most)
	}
	return os.ReadFile(resolved)
}

func darkSizes(l LookItem) map[string]string {
	if l.Avatar.Dark == nil {
		return nil
	}
	return l.Avatar.Dark.Sizes
}

// CheckLooks holds the looks of a manifest to what the hub checks of their files in the package at root: an
// extension's test calls it before it ships (the manifest's own rules: ValidateJSON("Manifest", manifest)).
func CheckLooks(manifest []byte, root string) error {
	var m struct {
		Contributes struct {
			Looks []LookItem `json:"looks"`
		} `json:"contributes"`
	}
	if err := json.Unmarshal(manifest, &m); err != nil {
		return err
	}
	for _, l := range m.Contributes.Looks {
		// Each key on its own: two sides may name the same file, and each must hold (a map by file would keep one).
		type pic struct {
			file       string
			side, most int
		}
		var pics []pic
		for _, sizes := range []map[string]string{l.Avatar.Sizes, darkSizes(l)} {
			for side, f := range sizes {
				n, _ := strconv.Atoi(side)
				pics = append(pics, pic{f, n, lookMaxSide})
			}
		}
		if d := l.Dot; d != nil {
			for _, st := range []*LookDotState{&d.States.Idle, d.States.Thinking, d.States.Working, d.States.NeedsYou, d.States.Offline} {
				if st != nil {
					pics = append(pics, pic{st.File, d.Size, lookMaxDotSide})
				}
			}
		}
		read := func(f string) ([]byte, error) { return ReadLookFile(root, f) }
		counted := map[string]bool{}
		var total int64
		for _, p := range pics {
			b, err := read(p.file)
			if err != nil {
				return fmt.Errorf("look %s: %v", l.ID, err)
			}
			if !counted[p.file] {
				counted[p.file] = true
				total += int64(len(b))
			}
			if err := CheckLookPNG(b, p.side, p.most); err != nil {
				return fmt.Errorf("look %s: %s: %v", l.ID, p.file, err)
			}
		}
		svgs := []string{l.Avatar.Svg}
		if l.Avatar.Dark != nil {
			svgs = append(svgs, l.Avatar.Dark.Svg)
		}
		for _, f := range svgs {
			if f == "" {
				continue
			}
			b, err := read(f)
			if err != nil {
				return fmt.Errorf("look %s: %v", l.ID, err)
			}
			if !counted[f] {
				counted[f] = true
				total += int64(len(b))
			}
			if _, err := CleanSVG(b); err != nil {
				return fmt.Errorf("look %s: %s: %v", l.ID, f, err)
			}
		}
		if total > LookMaxTotal {
			return fmt.Errorf("look %s: %d bytes, more than %d", l.ID, total, LookMaxTotal)
		}
	}
	return nil
}

// svgAllowed are the SVG elements a face is drawn with; anything else (script, foreignObject, use of another file,
// animation that sets attributes, links) is dropped with what is inside it.
var svgAllowed = map[string]bool{
	"svg": true, "g": true, "path": true, "circle": true, "ellipse": true, "rect": true, "line": true, "polyline": true,
	"polygon": true, "defs": true, "linearGradient": true, "radialGradient": true, "stop": true, "clipPath": true,
	"mask": true, "title": true, "desc": true, "symbol": true, "use": true, "filter": true, "feGaussianBlur": true,
	"feOffset": true, "feMerge": true, "feMergeNode": true, "feFlood": true, "feComposite": true, "feColorMatrix": true,
	"feBlend": true, "text": true, "tspan": true,
}

// CleanSVG is the SVG with what could run or reach out taken out: elements not in svgAllowed (script, foreignObject,
// style, image, a, animate…) with their content, on* attributes, hrefs that are not to the same document (#…), any
// url(…) that is not url(#…), and the DOCTYPE (no entities). An SVG that does not parse, has no <svg> root or whose
// root has neither a viewBox nor a size is refused.
func CleanSVG(raw []byte) ([]byte, error) {
	if len(raw) > LookMaxSVG {
		return nil, fmt.Errorf("%d bytes, more than %d", len(raw), LookMaxSVG)
	}
	dec := xml.NewDecoder(bytes.NewReader(raw))
	dec.Strict = true
	var out bytes.Buffer
	enc := xml.NewEncoder(&out)
	skip := 0
	depth := 0
	sawRoot := false
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("not an SVG: %v", err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if skip > 0 {
				skip++
				continue
			}
			if depth == 0 {
				if sawRoot || t.Name.Local != "svg" {
					return nil, errors.New("not an SVG: the root is not <svg>")
				}
				sawRoot = true
				if !hasAttr(t, "viewBox") && !(hasAttr(t, "width") && hasAttr(t, "height")) {
					return nil, errors.New("the <svg> has neither a viewBox nor a width and height")
				}
			}
			if !svgAllowed[t.Name.Local] {
				skip = 1
				continue
			}
			depth++
			t.Attr = cleanAttrs(t.Attr)
			if err := enc.EncodeToken(svgToken(t)); err != nil {
				return nil, err
			}
		case xml.EndElement:
			if skip > 0 {
				skip--
				continue
			}
			depth--
			if err := enc.EncodeToken(xml.EndElement{Name: localName(t.Name)}); err != nil {
				return nil, err
			}
		case xml.CharData:
			if skip == 0 && depth > 0 {
				if err := enc.EncodeToken(t.Copy()); err != nil {
					return nil, err
				}
			}
		case xml.Directive:
			// <!DOCTYPE …> and its entities: never written out.
			if bytes.Contains(bytes.ToUpper(t), []byte("ENTITY")) {
				return nil, errors.New("an SVG with entities")
			}
		}
		// Comments and processing instructions are left out.
	}
	if !sawRoot {
		return nil, errors.New("not an SVG: no <svg>")
	}
	if err := enc.Flush(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func hasAttr(t xml.StartElement, name string) bool {
	for _, a := range t.Attr {
		if a.Name.Local == name && a.Name.Space == "" {
			return true
		}
	}
	return false
}

// localName drops the namespace the decoder resolved: the cleaned SVG is written with the SVG namespace on its root.
func localName(n xml.Name) xml.Name { return xml.Name{Local: n.Local} }

func svgToken(t xml.StartElement) xml.StartElement {
	out := xml.StartElement{Name: localName(t.Name)}
	if t.Name.Local == "svg" {
		out.Attr = append(out.Attr, xml.Attr{Name: xml.Name{Local: "xmlns"}, Value: "http://www.w3.org/2000/svg"})
	}
	out.Attr = append(out.Attr, t.Attr...)
	return out
}

// cleanAttrs keeps the attributes that only draw: no on* handlers, no namespaced ones but xlink:href, hrefs and
// url()s only to the same document.
func cleanAttrs(attrs []xml.Attr) []xml.Attr {
	var out []xml.Attr
	for _, a := range attrs {
		name := strings.ToLower(a.Name.Local)
		switch {
		case strings.HasPrefix(name, "on"):
			continue
		case name == "xmlns" || a.Name.Space == "xmlns":
			continue // written again on the root
		case name == "href":
			if a.Name.Space != "" && a.Name.Space != "http://www.w3.org/1999/xlink" {
				continue
			}
			if !isSameDocumentFragment(a.Value) {
				continue
			}
			out = append(out, xml.Attr{Name: xml.Name{Local: "href"}, Value: a.Value})
			continue
		case a.Name.Space != "" && a.Name.Space != "http://www.w3.org/XML/1998/namespace":
			continue
		case name == "base":
			continue
		}
		if !sameDocumentURLs(a.Value, name == "style") {
			continue
		}
		out = append(out, xml.Attr{Name: xml.Name{Local: a.Name.Local}, Value: a.Value})
	}
	return out
}

func trimCSSSpace(s string) string {
	start := 0
	for start < len(s) && isCSSWhitespace(s[start]) {
		start++
	}
	end := len(s)
	for end > start && isCSSWhitespace(s[end-1]) {
		end--
	}
	return s[start:end]
}

func isSameDocumentFragment(s string) bool {
	s = trimCSSSpace(s)
	return len(s) > 1 && s[0] == '#'
}

func isCSSWhitespace(b byte) bool {
	return b == ' ' || b == '\t' || b == '\n' || b == '\r' || b == '\f'
}

func isHexDigit(b byte) bool {
	return (b >= '0' && b <= '9') || (b >= 'a' && b <= 'f') || (b >= 'A' && b <= 'F')
}

func hexDigitValue(b byte) rune {
	switch {
	case b >= '0' && b <= '9':
		return rune(b - '0')
	case b >= 'a' && b <= 'f':
		return rune(b-'a') + 10
	default:
		return rune(b-'A') + 10
	}
}

func toLowerASCII(r rune) rune {
	if r >= 'A' && r <= 'Z' {
		return r + ('a' - 'A')
	}
	return r
}

func isCSSIdentByte(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9') || b == '_' || b == '-' || b >= 0x80
}

func isValidCSSEscape(s string, i int) bool {
	return i+1 < len(s) && s[i] == '\\' && s[i+1] != '\n' && s[i+1] != '\r' && s[i+1] != '\f'
}

// consumeCSSEscape decodes one CSS Syntax L3 §4.3.7 escape starting at s[i] == '\\'.
func consumeCSSEscape(s string, i int) (rune, int) {
	i++
	if i >= len(s) {
		return '\uFFFD', i
	}
	if isHexDigit(s[i]) {
		var val rune
		digits := 0
		for i < len(s) && digits < 6 && isHexDigit(s[i]) {
			val = val*16 + hexDigitValue(s[i])
			i++
			digits++
		}
		if i < len(s) {
			if s[i] == '\r' && i+1 < len(s) && s[i+1] == '\n' {
				i += 2
			} else if isCSSWhitespace(s[i]) {
				i++
			}
		}
		if val == 0 || val > 0x10FFFF || (val >= 0xD800 && val <= 0xDFFF) {
			val = '\uFFFD'
		}
		return val, i
	}
	r, sz := utf8.DecodeRuneInString(s[i:])
	return r, i + sz
}

func consumeCSSIdent(s string, i int) (string, int) {
	var b strings.Builder
	for i < len(s) {
		if isCSSIdentByte(s[i]) {
			if s[i] >= 'A' && s[i] <= 'Z' {
				b.WriteByte(s[i] + ('a' - 'A'))
			} else {
				b.WriteByte(s[i])
			}
			i++
		} else if isValidCSSEscape(s, i) {
			r, next := consumeCSSEscape(s, i)
			b.WriteRune(toLowerASCII(r))
			i = next
		} else {
			break
		}
	}
	return b.String(), i
}

// consumeCSSString consumes a quoted CSS string per CSS Syntax L3 §4.3.5.
func consumeCSSString(s string, i int) (string, int, bool) {
	quote := s[i]
	i++
	var b strings.Builder
	for i < len(s) {
		c := s[i]
		if c == quote {
			return b.String(), i + 1, false
		}
		if c == '\n' || c == '\r' || c == '\f' {
			return b.String(), i, true
		}
		if c == '\\' {
			if i+1 >= len(s) {
				return b.String(), i + 1, false
			}
			if s[i+1] == '\r' && i+2 < len(s) && s[i+2] == '\n' {
				i += 3
				continue
			}
			if s[i+1] == '\n' || s[i+1] == '\r' || s[i+1] == '\f' {
				i += 2
				continue
			}
			r, next := consumeCSSEscape(s, i)
			b.WriteRune(r)
			i = next
			continue
		}
		b.WriteByte(c)
		i++
	}
	return b.String(), i, false
}

func isForbiddenCSSFunc(name string) bool {
	switch name {
	case "image-set", "-webkit-image-set", "-moz-image-set", "-o-image-set", "-ms-image-set",
		"image", "cross-fade", "-webkit-cross-fade", "src", "expression":
		return true
	}
	return false
}

func hasForbiddenScheme(s string) bool {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c <= 0x20 || c == 0x7f {
			continue
		}
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		b.WriteByte(c)
	}
	norm := b.String()
	return strings.Contains(norm, "javascript:") || strings.Contains(norm, "vbscript:")
}

func hasDangerousDecodedIdent(ident string) bool {
	if strings.ContainsRune(ident, '(') {
		for _, fn := range []string{
			"url(", "image-set(", "-webkit-image-set(", "-moz-image-set(", "-o-image-set(", "-ms-image-set(",
			"image(", "cross-fade(", "-webkit-cross-fade(", "src(", "expression(",
		} {
			if strings.Contains(ident, fn) {
				return true
			}
		}
	}
	if strings.ContainsRune(ident, ':') {
		if hasForbiddenScheme(ident) || strings.Contains(ident, "behavior:") || strings.Contains(ident, "-moz-binding:") {
			return true
		}
	}
	return false
}

func skipCSSSpaceAndComments(s string, i int) int {
	for i < len(s) {
		if isCSSWhitespace(s[i]) {
			i++
		} else if s[i] == '/' && i+1 < len(s) && s[i+1] == '*' {
			end := strings.Index(s[i+2:], "*/")
			if end < 0 {
				return len(s)
			}
			i += 2 + end + 2
		} else {
			break
		}
	}
	return i
}

// stripCSSComments removes top-level /* ... */ comments outside quoted strings in O(N) time.
func stripCSSComments(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	i := 0
	for i < len(s) {
		c := s[i]
		if c == '\'' || c == '"' {
			quote := c
			b.WriteByte(c)
			i++
			for i < len(s) {
				ch := s[i]
				if ch == quote {
					b.WriteByte(ch)
					i++
					break
				}
				if ch == '\n' || ch == '\r' || ch == '\f' {
					break
				}
				if ch == '\\' && i+1 < len(s) {
					if s[i+1] == '\r' && i+2 < len(s) && s[i+2] == '\n' {
						b.WriteString("\\\r\n")
						i += 3
					} else if s[i+1] == '\n' || s[i+1] == '\r' || s[i+1] == '\f' {
						b.WriteString(s[i : i+2])
						i += 2
					} else {
						_, next := consumeCSSEscape(s, i)
						b.WriteString(s[i:next])
						i = next
					}
					continue
				}
				b.WriteByte(ch)
				i++
			}
			continue
		}
		if c == '<' && strings.HasPrefix(s[i:], "<!--") {
			b.WriteString("<!--")
			i += 4
			continue
		}
		if c == '-' && strings.HasPrefix(s[i:], "-->") {
			b.WriteString("-->")
			i += 3
			continue
		}
		if c == '/' && i+1 < len(s) && s[i+1] == '*' {
			end := strings.Index(s[i+2:], "*/")
			if end < 0 {
				break
			}
			i += 2 + end + 2
			continue
		}
		b.WriteByte(c)
		i++
	}
	return b.String()
}

func checkCSSTokens(v string, isStyle bool) bool {
	for i := 0; i < len(v); {
		c := v[i]
		if isCSSWhitespace(c) {
			i++
			continue
		}
		if c == '/' && i+1 < len(v) && v[i+1] == '*' {
			end := strings.Index(v[i+2:], "*/")
			if end < 0 {
				break
			}
			i += 2 + end + 2
			continue
		}
		if c == '<' && strings.HasPrefix(v[i:], "<!--") {
			i += 4
			continue
		}
		if c == '-' && strings.HasPrefix(v[i:], "-->") {
			i += 3
			continue
		}
		if c == '\'' || c == '"' {
			str, next, bad := consumeCSSString(v, i)
			i = next
			if bad && isStyle {
				return false
			}
			if hasForbiddenScheme(str) {
				return false
			}
			continue
		}
		if c == '@' {
			i++
			if i < len(v) && (isCSSIdentByte(v[i]) || isValidCSSEscape(v, i)) {
				ident, next := consumeCSSIdent(v, i)
				i = next
				if strings.TrimSpace(ident) == "import" {
					return false
				}
			}
			continue
		}
		if isCSSIdentByte(c) || isValidCSSEscape(v, i) {
			ident, next := consumeCSSIdent(v, i)
			i = next
			if hasDangerousDecodedIdent(ident) {
				return false
			}
			if ident == "-moz-binding" {
				return false
			}
			if ident == "behavior" || ident == "javascript" || ident == "vbscript" {
				peek := skipCSSSpaceAndComments(v, i)
				if peek < len(v) && v[peek] == ':' {
					return false
				}
			}
			if i < len(v) && v[i] == '(' {
				i++
				if isForbiddenCSSFunc(ident) {
					return false
				}
				if ident == "url" {
					for i < len(v) && isCSSWhitespace(v[i]) {
						i++
					}
					if i >= len(v) {
						return false
					}
					if v[i] == '\'' || v[i] == '"' {
						rawURL, nextPos, bad := consumeCSSString(v, i)
						if bad || !isSameDocumentFragment(rawURL) {
							return false
						}
						i = nextPos
						for i < len(v) && isCSSWhitespace(v[i]) {
							i++
						}
						if i >= len(v) || v[i] != ')' {
							return false
						}
						i++
						continue
					}
					var u strings.Builder
					for i < len(v) {
						ch := v[i]
						if ch == ')' || isCSSWhitespace(ch) {
							break
						}
						if ch == '"' || ch == '\'' || ch == '(' || ch <= 0x1f || ch == 0x7f {
							return false
						}
						if ch == '\\' {
							if !isValidCSSEscape(v, i) {
								return false
							}
							r, nextPos := consumeCSSEscape(v, i)
							u.WriteRune(r)
							i = nextPos
							continue
						}
						u.WriteByte(ch)
						i++
					}
					for i < len(v) && isCSSWhitespace(v[i]) {
						i++
					}
					if i >= len(v) || v[i] != ')' {
						return false
					}
					i++
					if !isSameDocumentFragment(u.String()) {
						return false
					}
				}
			}
			continue
		}
		i++
	}
	return true
}

func sameDocumentURLs(v string, isStyle bool) bool {
	if !checkCSSTokens(v, isStyle) {
		return false
	}
	if strings.Contains(v, "/*") {
		if !checkCSSTokens(stripCSSComments(v), isStyle) {
			return false
		}
	}
	return true
}
