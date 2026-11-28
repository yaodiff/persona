package wire

import (
	"bytes"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The example looks extension (schema/extension/examples/example-looks) holds to the checks; a picture that does not,
// fails them.
func TestCheckLooks(t *testing.T) {
	root := filepath.Join("..", "..", "..", "schema", "extension", "examples", "example-looks")
	manifest, err := os.ReadFile(filepath.Join(root, "persona-extension.json"))
	if err != nil {
		t.Skip("not in the main repository:", err)
	}
	if err := ValidateJSON("Manifest", manifest); err != nil {
		t.Fatal(err)
	}
	if err := CheckLooks(manifest, root); err != nil {
		t.Fatal(err)
	}
	bad := strings.Replace(string(manifest), `"128": "looks/aurora/avatar-128.png"`, `"128": "looks/aurora/avatar-256.png"`, 1)
	if err := CheckLooks([]byte(bad), root); err == nil || !strings.Contains(err.Error(), "its key says 128") {
		t.Fatal(err)
	}
	if err := CheckLooks([]byte(strings.Replace(string(manifest), "avatar.svg", "../avatar.svg", 1)), root); err == nil {
		t.Fatal("a file out of the package passed")
	}
}

func TestCleanSVGDropsScripts(t *testing.T) {
	out, err := CleanSVG([]byte(`<svg viewBox="0 0 1 1" onload="x()"><script>x()</script><circle r="1"/></svg>`))
	if err != nil || strings.Contains(string(out), "x()") || !strings.Contains(string(out), "<circle") {
		t.Fatalf("%v %s", err, out)
	}
}

// Two sides naming one picture: each side is held to it (the hub does the same), every time, not one of them as a
// map's order falls (#3370).
func TestCheckLooksEverySide(t *testing.T) {
	root := t.TempDir()
	var b bytes.Buffer
	_ = png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 256, 256)))
	if err := os.WriteFile(filepath.Join(root, "a.png"), b.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	for name, looks := range map[string]string{
		"light": `{"id": "x", "name": "x", "avatar": {"sizes": {"128": "a.png", "256": "a.png"}}, "app_icon_fallback": "persona"}`,
		"dark":  `{"id": "x", "name": "x", "avatar": {"sizes": {"256": "a.png"}, "dark": {"sizes": {"128": "a.png"}}}, "app_icon_fallback": "persona"}`,
		"dot":   `{"id": "x", "name": "x", "avatar": {"sizes": {"256": "a.png"}}, "dot": {"size": 144, "states": {"idle": {"file": "a.png"}}}, "app_icon_fallback": "persona"}`,
	} {
		manifest := []byte(`{"contributes": {"looks": [` + looks + `]}}`)
		for i := 0; i < 20; i++ {
			if err := CheckLooks(manifest, root); err == nil {
				t.Fatalf("%s: a 256 px picture passed for another side (run %d)", name, i)
			}
		}
	}
	ok := []byte(`{"contributes": {"looks": [{"id": "x", "name": "x", "avatar": {"sizes": {"256": "a.png"}, "dark": {"sizes": {"256": "a.png"}}}, "app_icon_fallback": "persona"}]}}`)
	if err := CheckLooks(ok, root); err != nil {
		t.Fatal(err)
	}
}

func TestCleanSVGBlocksCSSBypassAndExternalImages(t *testing.T) {
	attacks := map[string]string{
		"hex escape url":                 `<svg viewBox="0 0 10 10"><rect width="1" height="1" style="fill: \75rl(https://evil.example/hex)"/></svg>`,
		"partial hex escape url":         `<svg viewBox="0 0 10 10"><rect width="1" height="1" style="fill: u\72l(https://evil.example/hex2)"/></svg>`,
		"six digit hex escape with ws":   `<svg viewBox="0 0 10 10"><rect width="1" height="1" style="fill: \000075 rl(https://evil.example/hex6)"/></svg>`,
		"uppercase hex escape url":       `<svg viewBox="0 0 10 10"><rect width="1" height="1" style="fill: \55\52\4c(https://evil.example/upper)"/></svg>`,
		"non-hex backslash escape url":   `<svg viewBox="0 0 10 10"><rect width="1" height="1" style="fill: \u\r\l(https://evil.example/nonhex)"/></svg>`,
		"escaped scheme inside url":      `<svg viewBox="0 0 10 10"><rect width="1" height="1" style="fill: url(\68ttps://evil.example/scheme)"/></svg>`,
		"line continuation inside url":   "<svg viewBox=\"0 0 10 10\"><rect width=\"1\" height=\"1\" style=\"fill: url('https://evil.example/\\\ncont')\"/></svg>",
		"comment split url":              `<svg viewBox="0 0 10 10"><rect width="1" height="1" style="fill: u/**/rl(https://evil.example/comment)"/></svg>`,
		"comment inside url":             `<svg viewBox="0 0 10 10"><rect width="1" height="1" style="fill: url(/**/https://evil.example/c2)"/></svg>`,
		"comment token inside string":    `<svg viewBox="0 0 10 10"><rect width="1" height="1" style="content: '/*'; background: url(https://evil.example/qc); --x: '*/'"/></svg>`,
		"unclosed comment inside string": `<svg viewBox="0 0 10 10"><rect width="1" height="1" style="content: '/*'; background: url(https://evil.example/uq)"/></svg>`,
		"image-set":                      `<svg viewBox="0 0 10 10"><rect width="1" height="1" style="background-image: image-set('https://evil.example/is.png' 1x)"/></svg>`,
		"webkit-image-set":               `<svg viewBox="0 0 10 10"><rect width="1" height="1" style="background-image: -webkit-image-set('https://evil.example/wis.png' 1x)"/></svg>`,
		"hex escaped image-set":          `<svg viewBox="0 0 10 10"><rect width="1" height="1" style="background-image: \69mage-set('https://evil.example/his.png' 1x)"/></svg>`,
		"css image()":                    `<svg viewBox="0 0 10 10"><rect width="1" height="1" style="background-image: image('https://evil.example/img.png')"/></svg>`,
		"css cross-fade()":               `<svg viewBox="0 0 10 10"><rect width="1" height="1" style="background-image: cross-fade(50% 'https://evil.example/cf.png', red)"/></svg>`,
		"css src()":                      `<svg viewBox="0 0 10 10"><rect width="1" height="1" style="background-image: src('https://evil.example/src.png')"/></svg>`,
		"escaped @import":                `<svg viewBox="0 0 10 10"><rect width="1" height="1" style="@\69mport 'https://evil.example/a.css'"/></svg>`,
		"escaped expression":             `<svg viewBox="0 0 10 10"><rect width="1" height="1" style="width: expr\65ssion(evil.example)"/></svg>`,
		"behavior property":              `<svg viewBox="0 0 10 10"><rect width="1" height="1" style="b\65havior: url(#evil.example)"/></svg>`,
		"moz-binding property":           `<svg viewBox="0 0 10 10"><rect width="1" height="1" style="-moz-binding: url(#evil.example)"/></svg>`,
		"escaped javascript":             `<svg viewBox="0 0 10 10"><rect width="1" height="1" style="fill: j\61vascript:evil.example"/></svg>`,
		"filter attr escaped url":        `<svg viewBox="0 0 10 10"><rect width="1" height="1" filter="\75rl(https://evil.example/f.svg#f)"/></svg>`,
		"fill attr escaped url":          `<svg viewBox="0 0 10 10"><rect width="1" height="1" fill="u\72l(https://evil.example/g.svg#g)"/></svg>`,
		"clip-path attr escaped url":     `<svg viewBox="0 0 10 10"><rect width="1" height="1" clip-path="\75rl(https://evil.example/c.svg#c)"/></svg>`,
		"mask attr image-set":            `<svg viewBox="0 0 10 10"><rect width="1" height="1" mask="image-set('https://evil.example/m.png' 1x)"/></svg>`,
		"marker-end attr non-hex url":    `<svg viewBox="0 0 10 10"><path d="M0 0" marker-end="\u\r\l(https://evil.example/a.svg#m)"/></svg>`,
		"second url after valid hash":    `<svg viewBox="0 0 10 10"><rect width="1" height="1" style="background: url('#ok'), url('https://evil.example/second')"/></svg>`,
		"url modifier after hash":        `<svg viewBox="0 0 10 10"><rect width="1" height="1" style="background: url('#ok' evil.example)"/></svg>`,
		"unclosed url":                   `<svg viewBox="0 0 10 10"><rect width="1" height="1" style="fill: url(https://evil.example/unclosed"/></svg>`,
		"empty hash url":                 `<svg viewBox="0 0 10 10"><rect width="1" height="1" style="fill: url(#); --x: evil.example"/></svg>`,
		"nbsp before hash in url":        `<svg viewBox="0 0 10 10"><rect width="1" height="1" style="fill: url('&#xA0;#evil.example')"/></svg>`,
		"css escaped nbsp in url":        `<svg viewBox="0 0 10 10"><rect width="1" height="1" style="fill: url(\a0 #evil.example)"/></svg>`,
		"nbsp before hash in href":       `<svg viewBox="0 0 10 10"><use href="&#xA0;#evil.example"/></svg>`,
		"xml entity plus css escape":     `<svg viewBox="0 0 10 10"><rect width="1" height="1" style="fill: &#x5c;75rl(https://evil.example/ent)"/></svg>`,
		"escaped open paren in url":      `<svg viewBox="0 0 10 10"><rect width="1" height="1" style="fill: foo\28 url\28 https://evil.example/p)"/></svg>`,
		"cdo before url":                 `<svg viewBox="0 0 10 10"><rect width="1" height="1" style="&lt;!--url(https://evil.example/cdo)"/></svg>`,
		"cdc before url":                 `<svg viewBox="0 0 10 10"><rect width="1" height="1" style="--&gt;url(https://evil.example/cdc)"/></svg>`,
		"hex escape newline before cmt":  "<svg viewBox=\"0 0 10 10\"><rect width=\"1\" height=\"1\" style=\"content: '\\a\n/*'; background: url(https://evil.example/hexnl); --x: '*/'\"/></svg>",
		"xml:base attribute":             `<svg viewBox="0 0 10 10"><rect width="1" height="1" xml:base="https://evil.example/"/></svg>`,
	}
	for name, svg := range attacks {
		t.Run(name, func(t *testing.T) {
			out, err := CleanSVG([]byte(svg))
			if err != nil {
				t.Fatalf("CleanSVG failed: %v", err)
			}
			if strings.Contains(string(out), "evil.example") {
				t.Fatalf("attack %s survived CleanSVG: %s", name, out)
			}
		})
	}

	valid := `<svg xmlns="http://www.w3.org/2000/svg" xmlns:xlink="http://www.w3.org/1999/xlink" viewBox="0 0 10 10">` +
		`<defs><linearGradient id="grad-1"><stop offset="0%" stop-color="#fff"/></linearGradient></defs>` +
		`<circle cx="5" cy="5" r="4" fill="url(#grad-1)" style="stroke: url('#grad-1'); opacity: 0.8; transform: translate(1px, 2px)"/>` +
		`<rect width="2" height="2" filter="url(#shadow)" clip-path="url(#clip)" aria-label="Bob's Avatar"/>` +
		`<use href="#grad-1"/><use xlink:href="#grad-1"/>` +
		`</svg>`
	clean, err := CleanSVG([]byte(valid))
	if err != nil {
		t.Fatalf("valid SVG rejected: %v", err)
	}
	s := string(clean)
	for _, want := range []string{
		`fill="url(#grad-1)"`,
		`style="stroke: url(&#39;#grad-1&#39;); opacity: 0.8; transform: translate(1px, 2px)"`,
		`filter="url(#shadow)"`,
		`clip-path="url(#clip)"`,
		`aria-label="Bob&#39;s Avatar"`,
		`<use href="#grad-1">`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("valid SVG lost %q in %s", want, s)
		}
	}
	again, err := CleanSVG(clean)
	if err != nil || !bytes.Equal(again, clean) {
		t.Fatalf("CleanSVG not idempotent: %v\n%s\n%s", err, clean, again)
	}
}
