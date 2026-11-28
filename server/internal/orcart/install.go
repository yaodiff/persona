package orcart

import (
	"os"
	"path/filepath"
	"runtime"

	"github.com/nwverse/persona/server/internal/lazyre"
)

// Installed reports whether Orca is on this machine, running or not, and the version its install says ("" when it does
// not say: only the macOS app's Info.plist does). Orca counts as installed when its app is where Orca itself looks for
// it (the macOS app in /Applications or ~/Applications; Orca.exe under %LOCALAPPDATA%\Programs or %ProgramFiles% on
// Windows; orca-ide in ~/.local/bin, /usr/local/bin, /usr/bin or /opt/Orca on Linux), or when a data directory of
// MetaPaths holds what a runtime left there (orca-runtime.json, or the CLI shim it writes under cli/bin): an orcad
// another computer's Orca installed over SSH has no app. PATH's `orca` is not read: on Linux it is often GNOME's
// screen reader.
func Installed() (bool, string) {
	home, _ := os.UserHomeDir()
	return installedIn(runtime.GOOS, home, os.Getenv, MetaPaths())
}

// installedIn is Installed for a platform, home folder, environment and metadata files (tests give their own).
func installedIn(goos, home string, env func(string) string, metaPaths []string) (bool, string) {
	var apps []string
	switch goos {
	case "darwin":
		bundles := []string{"/Applications/Orca.app"}
		if home != "" {
			bundles = append(bundles, filepath.Join(home, "Applications", "Orca.app"))
		}
		for _, b := range bundles {
			if isFile(filepath.Join(b, "Contents", "MacOS", "Orca")) {
				return true, bundleVersion(filepath.Join(b, "Contents", "Info.plist"))
			}
		}
	case "windows":
		if d := env("LOCALAPPDATA"); d != "" {
			apps = append(apps, filepath.Join(d, "Programs", "Orca", "Orca.exe"))
		}
		if d := env("ProgramFiles"); d != "" {
			apps = append(apps, filepath.Join(d, "Orca", "Orca.exe"))
		}
	default:
		if home != "" {
			apps = append(apps, filepath.Join(home, ".local", "bin", "orca-ide"))
		}
		apps = append(apps, "/usr/local/bin/orca-ide", "/usr/bin/orca-ide", "/opt/Orca/orca-ide")
	}
	for _, meta := range metaPaths {
		dir := filepath.Dir(meta)
		apps = append(apps, meta, filepath.Join(dir, "cli", "bin", "orca"), filepath.Join(dir, "cli", "bin", "orca.cmd"))
	}
	for _, f := range apps {
		if isFile(f) {
			return true, ""
		}
	}
	return false, ""
}

func isFile(path string) bool {
	st, err := os.Stat(path)
	return err == nil && !st.IsDir()
}

// plistVersion is CFBundleShortVersionString in an XML Info.plist.
var plistVersion = lazyre.New(`<key>CFBundleShortVersionString</key>\s*<string>([^<]{1,64})</string>`)

// bundleVersion is the app version an Info.plist names, "" when it cannot be read (a binary plist, say).
func bundleVersion(plist string) string {
	b, err := os.ReadFile(plist)
	if err != nil || len(b) > 1<<20 {
		return ""
	}
	if m := plistVersion.FindSubmatch(b); m != nil {
		return string(m[1])
	}
	return ""
}
