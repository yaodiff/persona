package orcart

import (
	"os"
	"path/filepath"
	"testing"
)

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// Orca is installed where its app is, or where a runtime left its data; nothing else (an `orca` on PATH, an empty data
// directory) counts. Only the macOS app says its version.
func TestInstalledIn(t *testing.T) {
	noEnv := func(string) string { return "" }
	home := t.TempDir()
	meta := filepath.Join(home, ".local", "share", "Orca", "orca-runtime.json")
	if ok, _ := installedIn("linux", home, noEnv, []string{meta}); ok {
		t.Fatal("installed with nothing there")
	}
	if err := os.MkdirAll(filepath.Dir(meta), 0o700); err != nil {
		t.Fatal(err)
	}
	if ok, _ := installedIn("linux", home, noEnv, []string{meta}); ok {
		t.Error("an empty data directory counts as installed")
	}
	writeFile(t, filepath.Join(filepath.Dir(meta), "cli", "bin", "orca"), "#!/bin/sh\n")
	if ok, v := installedIn("linux", home, noEnv, []string{meta}); !ok || v != "" {
		t.Errorf("orcad's CLI shim: %v %q", ok, v)
	}

	home = t.TempDir()
	writeFile(t, filepath.Join(home, ".local", "bin", "orca-ide"), "")
	if ok, _ := installedIn("linux", home, noEnv, nil); !ok {
		t.Error("the Linux app is not found")
	}

	home = t.TempDir()
	app := filepath.Join(home, "Applications", "Orca.app", "Contents")
	writeFile(t, filepath.Join(app, "MacOS", "Orca"), "")
	writeFile(t, filepath.Join(app, "Info.plist"), "<plist><dict><key>CFBundleName</key><string>Orca</string>\n"+
		"<key>CFBundleShortVersionString</key>\n\t<string>1.4.277</string></dict></plist>")
	if ok, v := installedIn("darwin", home, noEnv, nil); !ok || v != "1.4.277" {
		t.Errorf("the macOS app: %v %q", ok, v)
	}

	local := t.TempDir()
	writeFile(t, filepath.Join(local, "Programs", "Orca", "Orca.exe"), "")
	env := func(k string) string {
		if k == "LOCALAPPDATA" {
			return local
		}
		return ""
	}
	if ok, _ := installedIn("windows", t.TempDir(), env, nil); !ok {
		t.Error("the Windows app is not found")
	}
}
