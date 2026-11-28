package extmgr

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	sdk "github.com/nwverse/persona/sdk/go"
	"github.com/nwverse/persona/server/internal/clitext"
	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/extension/conformance"
	"github.com/nwverse/persona/server/internal/i18n"
)

// Writing an extension (docs/extensions/extensions.md "Writing extensions: SDK, templates and command line"): `personad extension new |
// test | pack` need no hub; `dev` hangs the extension on this machine's (extension_dev.go). What they print reads in
// the terminal's language (i18n.Terminal, #5520).

const extensionBuildUsage = `
writing one:
  new <id> [--template go|ts] [--name <name>] [--dir <directory>]
                                    a new extension from a template, with a copy of the SDK
  dev [<directory>]                 run it on this machine's hub from its directory: rebuilt and restarted when
                                    its files change, its log and the hub's calls to it printed here; Ctrl-C ends
                                    (settings and secrets from extension.dev.json: {"config": {…}, "secrets": {…}})
  test [<directory>]                build it for this machine and hold it to the hub's checks
  pack [<directory>] [--platforms linux-amd64,…] [--out dist] [--split]
                                    build every platform and pack it; prints the sha256 and the index entry
publishing a private market:
  keygen --out <file>               a new Ed25519 signing key: the private key to <file> (0600, PKCS #8 PEM), its
                                    public key printed for market add --key / market key add
  sign-index <persona-extensions.json> --key <file> [--key <file>…] [--out <file>]
                                    sign the index with the official envelope format (several keys while rotating)`

// extensionBuildCommand runs new, test and pack.
func extensionBuildCommand(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	opts, positional := buildFlags(args[1:])
	switch args[0] {
	case "new":
		if len(positional) < 1 {
			return errors.New(clitext.Named("usage: personad extension new <id> [--template go|ts] [--name <name>] [--dir <directory>]"))
		}
		return newExtension(positional[0], opts["template"], opts["name"], opts["dir"], stdout)
	case "test":
		return testExtension(ctx, dirArg(positional), stdout, stderr)
	case "pack":
		return packExtension(ctx, dirArg(positional), opts, stdout, stderr)
	}
	return errors.New(extensionUsage)
}

// buildFlags splits --name value / --name=value flags from the positional arguments.
func buildFlags(args []string) (map[string]string, []string) {
	opts := map[string]string{}
	var positional []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		name, ok := strings.CutPrefix(a, "--")
		if !ok {
			positional = append(positional, a)
			continue
		}
		if k, v, ok := strings.Cut(name, "="); ok {
			opts[k] = v
		} else if name == "split" || i+1 >= len(args) {
			opts[name] = "true"
		} else {
			opts[name] = args[i+1]
			i++
		}
	}
	return opts, positional
}

func dirArg(positional []string) string {
	if len(positional) > 0 {
		return positional[0]
	}
	return "."
}

// newExtension writes a new extension from a template, with the SDK copied in (sdk/go or sdk/ts).
func newExtension(id, template, name, dir string, stdout io.Writer) error {
	l := i18n.Terminal()
	if !extension.ValidID(id) || len(id) > 26 {
		return errors.New(i18n.T(l, "extmgr-cli.new_bad_id", i18n.Args{"id": strconv.Quote(id)}))
	}
	if template == "" {
		template = "go"
	}
	if template == "deno" {
		return newSourceExtension(id, name, dir, stdout)
	}
	if template != "go" && template != "ts" {
		return errors.New(i18n.T(l, "extmgr-cli.new_bad_template", i18n.Args{"template": strconv.Quote(template)}))
	}
	if name == "" {
		name = id
	}
	if n := len([]rune(name)); n > 40 || strings.ContainsAny(name, "\"\\\n") {
		return errors.New(i18n.T(l, "extmgr-cli.new_bad_name"))
	}
	if dir == "" {
		dir = id
	}
	if entries, err := os.ReadDir(dir); err == nil && len(entries) > 0 {
		return errors.New(i18n.T(l, "extmgr-cli.new_dir_not_empty", i18n.Args{"dir": dir}))
	}
	files, err := bundleFiles()
	if err != nil {
		return err
	}
	replace := strings.NewReplacer("{{id}}", id, "{{name}}", name)
	prefix := "templates/" + template + "/"
	n := 0
	for path, data := range files {
		rel, ok := strings.CutPrefix(path, prefix)
		if !ok {
			continue
		}
		if r, ok := strings.CutSuffix(rel, ".tmpl"); ok {
			rel = r
		}
		if err := writeNew(filepath.Join(dir, filepath.FromSlash(rel)), []byte(replace.Replace(string(data)))); err != nil {
			return err
		}
		n++
	}
	if n == 0 {
		return errors.New(i18n.T(l, "extmgr-cli.new_no_template", i18n.Args{"template": template}))
	}
	if err := copySDK(dir, template, files); err != nil {
		return err
	}
	fmt.Fprintln(stdout, i18n.T(l, "extmgr-cli.new_done", i18n.Args{"dir": dir, "id": id, "template": template}))
	fmt.Fprintf(stdout, "  cd %s\n", dir)
	step := func(command, key string) { fmt.Fprintf(stdout, "  %-29s %s\n", command, i18n.T(l, key)) }
	if template == "go" {
		step("go test ./...", "extmgr-cli.new_step_go_test")
	} else {
		step("bun test", "extmgr-cli.new_step_bun_test")
	}
	step(clitext.Named("personad extension dev"), "extmgr-cli.new_step_dev")
	step(clitext.Named("personad extension pack"), "extmgr-cli.new_step_pack")
	return nil
}

// copySDK puts the SDK the extension is written against under dir/sdk (go or ts).
func copySDK(dir, template string, bundle map[string][]byte) error {
	if template == "ts" {
		for path, data := range bundle {
			if rel, ok := strings.CutPrefix(path, "ts/"); ok {
				if err := writeNew(filepath.Join(dir, "sdk", "ts", filepath.FromSlash(rel)), data); err != nil {
					return err
				}
			}
		}
		return nil
	}
	to := filepath.Join(dir, "sdk", "go")
	if err := writeNew(filepath.Join(to, "go.mod"), []byte(sdk.GoMod)); err != nil {
		return err
	}
	return fs.WalkDir(sdk.Go, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := sdk.Go.ReadFile(path)
		if err != nil {
			return err
		}
		return writeNew(filepath.Join(to, filepath.FromSlash(path)), data)
	})
}

func writeNew(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// bundleFiles are the TypeScript SDK and the templates (sdk.Bundle), by path.
func bundleFiles() (map[string][]byte, error) {
	zr, err := gzip.NewReader(bytes.NewReader(sdk.Bundle))
	if err != nil {
		return nil, err
	}
	out := map[string][]byte{}
	tr := tar.NewReader(zr)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return out, nil
		}
		if err != nil {
			return nil, err
		}
		if h.Typeflag != tar.TypeReg {
			continue
		}
		data, err := io.ReadAll(tr)
		if err != nil {
			return nil, err
		}
		out[h.Name] = data
	}
}

// devProject is an extension's source directory: its manifest and how it is built. lang is the terminal's, what its
// build says what went wrong in.
type devProject struct {
	dir      string
	manifest *extension.Manifest
	kind     string // go | ts
	lang     i18n.Lang
}

func openProject(dir string) (*devProject, error) {
	l := i18n.Terminal()
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(extension.ManifestPath(abs))
	if err != nil {
		return nil, errors.New(i18n.T(l, "extmgr-cli.no_manifest", i18n.Args{"dir": abs}))
	}
	m, err := extension.Parse(data)
	if err != nil {
		return nil, err
	}
	p := &devProject{dir: abs, manifest: m, lang: l}
	switch {
	case m.Runtime == nil:
		p.kind = "" // declarations only: nothing to build
	case m.Runtime.IsDeno():
		p.kind = "deno"
	case exists(filepath.Join(abs, "go.mod")):
		p.kind = "go"
	case exists(filepath.Join(abs, "src", "main.ts")):
		p.kind = "ts"
	case goModule(abs):
		p.kind = "go"
	default:
		return nil, errors.New(i18n.T(l, "extmgr-cli.unknown_build"))
	}
	return p, nil
}

func exists(path string) bool { _, err := os.Stat(path); return err == nil }

// goModule: dir is in a Go module, its own or a repository's whose go.mod is further up (the extension repositories
// keep one go.mod at the root for every extensions/<id>/).
func goModule(dir string) bool {
	for d := dir; ; d = filepath.Dir(d) {
		if exists(filepath.Join(d, "go.mod")) {
			return true
		}
		if exists(filepath.Join(d, ".git")) || filepath.Dir(d) == d {
			return false
		}
	}
}

// platforms are those the manifest has a program for (this machine's, for a single command).
func (p *devProject) platforms() []string {
	if p.manifest.Runtime == nil {
		return nil
	}
	if p.manifest.Runtime.IsDeno() {
		return []string{"linux-amd64", "linux-arm64"}
	}
	if ps := p.manifest.Runtime.Command.Platforms(); len(ps) > 0 {
		return ps
	}
	return []string{runtime.GOOS + "-" + runtime.GOARCH}
}

// build builds the program for platform (goos-goarch) where the manifest says it is.
func (p *devProject) build(ctx context.Context, platform string, stderr io.Writer) error {
	if p.kind == "deno" {
		return p.buildSource(ctx, stderr)
	}
	if p.kind == "" {
		return nil
	}
	rel, ok := p.manifest.Runtime.Command.For(platform)
	if !ok {
		return errors.New(i18n.T(p.lang, "extmgr-cli.no_command", i18n.Args{"platform": platform}))
	}
	out := filepath.Join(p.dir, filepath.FromSlash(rel))
	goos, goarch, _ := strings.Cut(platform, "-")
	var cmd *exec.Cmd
	if p.kind == "go" {
		cmd = exec.CommandContext(ctx, "go", "build", "-buildvcs=false", "-trimpath", "-ldflags", "-s -w", "-o", out, ".")
		cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS="+goos, "GOARCH="+goarch)
	} else {
		arch := map[string]string{"amd64": "x64", "arm64": "arm64"}[goarch]
		if arch == "" {
			return errors.New(i18n.T(p.lang, "extmgr-cli.bun_platform", i18n.Args{"platform": platform}))
		}
		cmd = exec.CommandContext(ctx, "bun", "build", "--compile", "--minify", "--target=bun-"+goos+"-"+arch, "src/main.ts", "--outfile", out)
	}
	cmd.Dir = p.dir
	var buf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &buf, &buf
	if err := cmd.Run(); err != nil {
		_, _ = stderr.Write(buf.Bytes())
		var ee *exec.Error
		if errors.As(err, &ee) {
			return &buildError{i18n.T(p.lang, "extmgr-cli.build_needs", i18n.Args{"tool": cmd.Args[0], "error": err}), err}
		}
		return &buildError{i18n.T(p.lang, "extmgr-cli.build_failed", i18n.Args{"platform": platform, "error": err}), err}
	}
	if p.kind == "ts" && platform == "darwin-arm64" && runtime.GOOS == "darwin" {
		return signedForMac(ctx, out)
	}
	return nil
}

// codesign runs Apple's codesign; a variable so tests can stand in for it.
var codesign = func(ctx context.Context, args ...string) error {
	if _, err := exec.LookPath("codesign"); err != nil {
		return errNoCodesign
	}
	b, err := exec.CommandContext(ctx, "codesign", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(b)))
	}
	return nil
}

var errNoCodesign = errors.New("no codesign")

// errBunUnsigned: the bun here compiles Mac programs macOS will not run (signedForMac).
var errBunUnsigned = errors.New("bun left the program unsigned")

// signedForMac makes sure a program bun compiled for an Apple-silicon Mac is signed: macOS kills an unsigned arm64
// program the moment it starts. bun signs what it compiles; some versions (1.3.12 from npm, 2026-11) leave it unsigned
// in a form codesign cannot sign either, and the extension then dies with no word (#2555).
func signedForMac(ctx context.Context, out string) error {
	if err := codesign(ctx, "-v", out); err == nil || errors.Is(err, errNoCodesign) {
		return nil
	}
	if err := codesign(ctx, "--force", "--sign", "-", out); err == nil {
		if err := codesign(ctx, "-v", out); err == nil {
			return nil
		}
	}
	version := "?"
	if b, err := exec.CommandContext(ctx, "bun", "--version").Output(); err == nil {
		version = strings.TrimSpace(string(b))
	}
	return &buildError{i18n.T(i18n.Terminal(), "extmgr-cli.bun_unsigned", i18n.Args{"version": version, "program": filepath.Base(out),
		"error": errBunUnsigned}), errBunUnsigned}
}

// buildError is a build's failure in the terminal's language, wrapping its cause (errors.Is sees errBunUnsigned).
type buildError struct {
	text string
	err  error
}

func (e *buildError) Error() string { return e.text }
func (e *buildError) Unwrap() error { return e.err }

// check holds the directory to what installing it would (the program for this machine is there, the settings'
// schema, the inbox's rules), without copying it.
func (p *devProject) check() (*extension.Extension, *extension.Registry, error) {
	reg := extension.NewRegistry(filepath.Join(os.TempDir(), "persona-extension-check"), nil, nil, 0)
	cfg := json.RawMessage(nil)
	if dc, err := readDevConfig(p.dir); err == nil {
		cfg = dc.Config
	}
	e, err := reg.AddDev(p.dir, cfg, nil)
	return e, reg, err
}

// testExtension builds the extension for this machine and holds the program to the hub's checks.
func testExtension(ctx context.Context, dir string, stdout, stderr io.Writer) error {
	p, err := openProject(dir)
	if err != nil {
		return err
	}
	here := runtime.GOOS + "-" + runtime.GOARCH
	if err := p.build(ctx, here, stderr); err != nil {
		return err
	}
	l := p.lang
	e, reg, err := p.check()
	if err != nil {
		return &buildError{i18n.T(l, "extmgr-cli.check_failed", i18n.Args{"error": err}), err}
	}
	fmt.Fprintln(stdout, "✓ "+i18n.T(l, "extmgr-cli.check_ok", i18n.Args{"id": p.manifest.ID, "version": p.manifest.Version}))
	for _, w := range e.Warnings {
		fmt.Fprintln(stdout, "  "+i18n.T(l, "extmgr-cli.warning", i18n.Args{"text": w}))
	}
	rt := p.manifest.Runtime
	if rt.IsDeno() {
		return errors.New(i18n.T(l, "extension-source.package_check_only"))
	}
	if rt == nil || !rt.SpeaksRPC() {
		fmt.Fprintln(stdout, "✓ "+i18n.T(l, "extmgr-cli.check_no_protocol"))
		return nil
	}
	rel, _ := rt.Command.For(here)
	cmd := exec.CommandContext(ctx, filepath.Join(p.dir, filepath.FromSlash(rel)), rt.Args...)
	cmd.Dir, cmd.Stderr = p.dir, stderr
	data := filepath.Join(os.TempDir(), "persona-extension-test-"+p.manifest.ID)
	_ = os.MkdirAll(data, 0o700)
	cmd.Env = append(os.Environ(), "PERSONA_EXTENSION_ID="+p.manifest.ID, "PERSONA_EXTENSION_DIR="+p.dir, "PERSONA_EXTENSION_DATA="+data)
	in, _ := cmd.StdinPipe()
	out, _ := cmd.StdoutPipe()
	if err := cmd.Start(); err != nil {
		return err
	}
	defer func() { _ = cmd.Process.Kill() }()
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	r := &reporter{}
	err = r.run(func() {
		conformance.Check(r, p.manifest, conformance.Run{ToProgram: in, FromProgram: out, Exited: exited},
			extension.InitializeParams{Config: reg.Config(e), Hub: extension.HubInfo{Role: "center", Timezone: "Asia/Shanghai"}})
	})
	if err != nil {
		return &buildError{"✗ " + i18n.T(l, "extmgr-cli.protocol_failed", i18n.Args{"error": err}), err}
	}
	fmt.Fprintln(stdout, "✓ "+i18n.T(l, "extmgr-cli.protocol_ok"))
	return nil
}

// reporter is conformance.T outside go test: the first failure ends the check.
type reporter struct{}

type failed struct{ msg string }

func (reporter) Helper() {}
func (reporter) Fatalf(format string, args ...any) {
	panic(failed{fmt.Sprintf(format, args...)})
}

func (reporter) run(f func()) (err error) {
	defer func() {
		if v := recover(); v != nil {
			f, ok := v.(failed)
			if !ok {
				panic(v)
			}
			err = errors.New(f.msg)
		}
	}()
	f()
	return nil
}

// packExtension builds every platform and packs the extension: one package for all of them, or one each (--split).
func packExtension(ctx context.Context, dir string, opts map[string]string, stdout, stderr io.Writer) error {
	p, err := openProject(dir)
	if err != nil {
		return err
	}
	platforms := p.platforms()
	if s := opts["platforms"]; s != "" {
		platforms = strings.Split(s, ",")
	}
	for i, pl := range platforms {
		if p.kind == "deno" && i > 0 {
			continue // One platform-independent artifact serves every package platform.
		}
		fmt.Fprintln(stdout, i18n.T(p.lang, "extmgr-cli.pack_building", i18n.Args{"platform": pl}))
		if err := p.build(ctx, pl, stderr); err != nil {
			return err
		}
	}
	if _, _, err := p.check(); err != nil {
		return &buildError{i18n.T(p.lang, "extmgr-cli.check_failed", i18n.Args{"error": err}), err}
	}
	outDir := opts["out"]
	if outDir == "" {
		outDir = filepath.Join(p.dir, "dist")
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return err
	}
	type pkg struct {
		Platforms []string `json:"platforms,omitempty"`
		URL       string   `json:"url"`
		SHA256    string   `json:"sha256"`
		Size      int64    `json:"size"`
	}
	var pkgs []pkg
	groups := [][]string{platforms}
	if opts["split"] == "true" {
		groups = nil
		for _, pl := range platforms {
			groups = append(groups, []string{pl})
		}
	}
	for _, group := range groups {
		name := p.manifest.ID + "-" + p.manifest.Version
		if len(groups) > 1 {
			name += "-" + group[0]
		}
		name += ".tar.gz"
		data, err := p.tarball(group)
		if err != nil {
			return err
		}
		path := filepath.Join(outDir, name)
		if err := os.WriteFile(path, data, 0o644); err != nil {
			return err
		}
		sum := sha256.Sum256(data)
		pkgs = append(pkgs, pkg{Platforms: group, URL: name, SHA256: hex.EncodeToString(sum[:]), Size: int64(len(data))})
		fmt.Fprintln(stdout, i18n.T(p.lang, "extmgr-cli.pack_file", i18n.Args{"sha256": hex.EncodeToString(sum[:]), "path": path, "count": len(data)}))
	}
	fields := map[string]any{"packages": pkgs}
	// the translations go into the index entry too, so a market shows them before anything is downloaded (#897)
	translations, warnings := extension.ReadTranslations(p.dir)
	for _, w := range warnings {
		fmt.Fprintln(stderr, w)
	}
	if len(translations) > 0 {
		fields["l10n"] = translations
	}
	entry, _ := json.MarshalIndent(fields, "", "  ")
	fmt.Fprintln(stdout, i18n.T(p.lang, "extmgr-cli.pack_entry"))
	fmt.Fprintln(stdout, string(entry))
	return nil
}

// tarball packs the manifest, the programs of these platforms and the package's own files (skills, assistant,
// assets, translations, README, LICENSE) under one top directory; source code and the SDK copy stay out.
func (p *devProject) tarball(platforms []string) ([]byte, error) {
	// The manifest goes in under both names, without its tags (extension.PackagedManifest): hubs from before the
	// rename (#1844) read only persona-plugin.json, and hubs from before tags refuse a manifest with them.
	raw, err := os.ReadFile(extension.ManifestPath(p.dir))
	if err != nil {
		return nil, err
	}
	manifest, err := extension.PackagedManifest(raw)
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, name := range []string{"README.md", "LICENSE"} {
		if exists(filepath.Join(p.dir, name)) {
			paths = append(paths, name)
		}
	}
	if rt := p.manifest.Runtime; rt != nil {
		if rt.IsDeno() {
			paths = append(paths, filepath.FromSlash(rt.Source.Bundle))
		}
		for _, pl := range platforms {
			if rel, ok := rt.Command.For(pl); ok && !extension.IsBuiltinCommand(rel) {
				paths = append(paths, filepath.FromSlash(rel))
			}
		}
	}
	for _, d := range []string{"skills", "assistant", "assets", extension.L10nDir} {
		_ = filepath.WalkDir(filepath.Join(p.dir, d), func(path string, e fs.DirEntry, err error) error {
			if err == nil && !e.IsDir() {
				rel, _ := filepath.Rel(p.dir, path)
				paths = append(paths, rel)
			}
			return nil
		})
	}
	slices.Sort(paths)
	paths = slices.Compact(paths)
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	top := p.manifest.ID + "-" + p.manifest.Version + "/"
	for _, name := range []string{extension.ManifestFile, extension.LegacyManifestFile} {
		if err := tw.WriteHeader(&tar.Header{Name: top + name, Mode: 0o644, Size: int64(len(manifest)), ModTime: time.Unix(0, 0), Typeflag: tar.TypeReg}); err != nil {
			return nil, err
		}
		if _, err := tw.Write(manifest); err != nil {
			return nil, err
		}
	}
	for _, rel := range paths {
		full := filepath.Join(p.dir, rel)
		st, err := os.Stat(full)
		if err != nil {
			return nil, err
		}
		mode := int64(0o644)
		if st.Mode()&0o111 != 0 {
			mode = 0o755
		}
		if err := tw.WriteHeader(&tar.Header{Name: top + filepath.ToSlash(rel), Mode: mode, Size: st.Size(), ModTime: time.Unix(0, 0), Typeflag: tar.TypeReg}); err != nil {
			return nil, err
		}
		f, err := os.Open(full)
		if err != nil {
			return nil, err
		}
		_, err = io.Copy(tw, f)
		f.Close()
		if err != nil {
			return nil, err
		}
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// devConfig is extension.dev.json: the settings and secrets of an extension being written (keep it out of git).
type devConfig struct {
	Config  json.RawMessage   `json:"config,omitempty"`
	Secrets map[string]string `json:"secrets,omitempty"`
}

func readDevConfig(dir string) (devConfig, error) {
	var c devConfig
	data, err := os.ReadFile(filepath.Join(dir, "extension.dev.json"))
	if err != nil {
		return c, err
	}
	return c, json.Unmarshal(data, &c)
}
