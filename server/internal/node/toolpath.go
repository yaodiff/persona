package node

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
)

// Tools a version manager installs (mise, asdf) are reached through its shims directory, which the user's shell rc
// file puts on PATH. The node's PATH comes from a service (launchd, systemd), an ssh session or the hub, and the herdr
// server it starts gets a clean login shell's (herdrServerEnv), which skips the interactive rc file mise is usually
// activated in: git there cannot run git-lfs, and a worktree of an LFS repository fails (#10127). So both get the
// shims directories that exist, after what PATH has already (a tool only a version manager has is found; one found
// before is still the one that runs), and the directories hub.json lists for the computer (machines[].extra_path,
// node.configure extra_path), in front. The node adds both itself (the shims when it starts, the configured ones when
// the hub hands them over), whatever started it, so no service file holds them and a rerun of setup cannot drop them
// (docs/operations/config.md "hub runtime config (hub.json)").

// versionManagerShims are the shims directories of mise and asdf that exist, where their data-directory variables
// or their defaults put them.
func versionManagerShims(home string, getenv func(string) string) []string {
	mise := getenv("MISE_DATA_DIR")
	if mise == "" {
		if xdg := getenv("XDG_DATA_HOME"); xdg != "" {
			mise = filepath.Join(xdg, "mise")
		} else {
			mise = filepath.Join(home, ".local", "share", "mise")
		}
	}
	asdf := getenv("ASDF_DATA_DIR")
	if asdf == "" {
		asdf = filepath.Join(home, ".asdf")
	}
	var out []string
	for _, d := range []string{filepath.Join(mise, "shims"), filepath.Join(asdf, "shims")} {
		if st, err := os.Stat(d); err == nil && st.IsDir() && filepath.IsAbs(d) && !slices.Contains(out, d) {
			out = append(out, d)
		}
	}
	return out
}

// withDirsAfter is the PATH value path with those of dirs it lacks added at its end.
func withDirsAfter(path string, dirs []string) string {
	list := filepath.SplitList(path)
	for _, d := range dirs {
		if !slices.ContainsFunc(list, func(p string) bool { return p != "" && filepath.Clean(p) == d }) {
			list = append(list, d)
		}
	}
	return strings.Join(list, string(os.PathListSeparator))
}

// extraPath is the node's part of PATH that node.configure sets: base is PATH as New made it (the first change's
// PATH when New did not run), dirs what hub.json's machines[].extra_path last said, in front of base. PATH is the
// process's, so this is too.
var extraPath struct {
	mu   sync.Mutex
	base *string
	dirs []string
}

// withoutExtraPath is path without the directories setExtraPath put in front of it, while path is still the PATH New
// or setExtraPath made: a resident node (persona-node attach) makes a new Node for each hub connection in one process,
// and the next one starts from that base, not with the directories hub.json added for the last one baked in (which [])
// could then never take out, review of #10131). resident is false for a PATH someone else set since, or before any New.
func withoutExtraPath(path string) (base string, resident bool) {
	extraPath.mu.Lock()
	defer extraPath.mu.Unlock()
	if extraPath.base == nil {
		return path, false
	}
	made := *extraPath.base
	if len(extraPath.dirs) > 0 {
		list := slices.Clone(extraPath.dirs)
		if made != "" {
			list = append(list, made)
		}
		made = strings.Join(list, string(os.PathListSeparator))
	}
	if path != made {
		return path, false
	}
	return *extraPath.base, true
}

// resetExtraPath makes path the node's PATH without configured directories (New).
func resetExtraPath(path string) {
	extraPath.mu.Lock()
	defer extraPath.mu.Unlock()
	extraPath.base, extraPath.dirs = &path, nil
}

// setExtraPath puts the directories hub.json lists for this computer (machines[].extra_path) at the front of the
// node's PATH, in place of the ones it was told before; none takes them out again. "~/" is this computer's home; an
// entry that is neither that nor absolute (a relative one, a $VARIABLE) is left out, as a directory the node cannot
// name for certain is none to run programs from.
func (n *Node) setExtraPath(dirs []string) {
	var keep []string
	for _, d := range dirs {
		d = strings.TrimSpace(d)
		if rest, ok := strings.CutPrefix(d, "~/"); ok && n.home != "" {
			d = filepath.Join(n.home, rest)
		} else if d == "~" && n.home != "" {
			d = n.home
		}
		if !filepath.IsAbs(d) || strings.ContainsAny(d, "$`"+string(os.PathListSeparator)) {
			continue
		}
		if d = filepath.Clean(d); !slices.Contains(keep, d) {
			keep = append(keep, d)
		}
	}
	extraPath.mu.Lock()
	if extraPath.base == nil {
		base := os.Getenv("PATH")
		extraPath.base = &base
	}
	if slices.Equal(keep, extraPath.dirs) {
		extraPath.mu.Unlock()
		return
	}
	extraPath.dirs = keep
	list := slices.Clone(keep)
	if *extraPath.base != "" { // an empty one would add "", the working directory
		list = append(list, *extraPath.base)
	}
	os.Setenv("PATH", strings.Join(list, string(os.PathListSeparator)))
	extraPath.mu.Unlock()
	invalidateLauncherCache()
}

// herdrToolDirs are what the herdr server's login shell starts with after the system's directories
// (herdrServerEnv): the configured directories, then the version managers' shims.
func herdrToolDirs(home string) []string {
	extraPath.mu.Lock()
	dirs := slices.Clone(extraPath.dirs)
	extraPath.mu.Unlock()
	for _, d := range versionManagerShims(home, os.Getenv) {
		if !slices.Contains(dirs, d) {
			dirs = append(dirs, d)
		}
	}
	return dirs
}
