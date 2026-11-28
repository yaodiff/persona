package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/nodeproto"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/update"
)

// runRelease is `personad release …`, what scripts/release/publish.sh runs to describe and check a release (docs/operations/hub-update.md "Public publishing").
func runRelease(args []string) error {
	if len(args) == 0 {
		return usageError(releaseUsage())
	}
	switch args[0] {
	case "manifest":
		fs := flag.NewFlagSet("release manifest", flag.ContinueOnError)
		dir := fs.String("dir", "", "where the personad-<os>-<arch>.gz are")
		ver := fs.String("version", "", "the version")
		num := fs.String("build", "", "the build number (UTC YYYYMMDDHHMM)")
		commit := fs.String("commit", "", "the commit (full id; run in its repository: its ancestors go in too)")
		channel := fs.String("channel", "release", "release or beta (a beta versioned prerelease)")
		notes := fs.String("notes", "", "what is new, for the app")
		minApp := fs.Int("min-app", 0, "the oldest app build that works with it")
		out := fs.String("out", "", "where to write latest.json")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if err := noReleaseArgs("release manifest", fs.Args()); err != nil {
			return err
		}
		if *dir == "" || *out == "" {
			return usageError("release manifest needs --dir and --out\n" + releaseUsage())
		}
		n, err := update.ParseNumber(*num)
		if err != nil {
			return err
		}
		// Every commit the release contains, so a hub deployed from newer code than this does not take it by itself.
		revs, err := exec.Command("git", "rev-list", *commit).Output()
		if err != nil {
			return fmt.Errorf("git rev-list %s: %w", *commit, err)
		}
		ancestors, err := update.EncodeAncestors(strings.Split(string(revs), "\n"))
		if err != nil {
			return err
		}
		// The release's nodes are this build's: they announce its node protocol (managers of other builds check it).
		m, err := update.BuildManifest(*dir, update.Manifest{Version: *ver, Build: n, Commit: *commit, Released: time.Now().UTC().Truncate(time.Second),
			MinAppBuild: *minApp, Protocol: proto.Protocol, Notes: *notes, Ancestors: ancestors, NodeProtocol: nodeproto.Version,
			NodeProtocolMin: nodeproto.MinManagerVersion, NodeCapabilities: nodeproto.Announced,
			// The manifest is this program's edition: publish.sh writes each edition's with that edition's personad.
			Edition: update.ThisEdition.Marker()})
		if err != nil {
			return err
		}
		m.Channel = *channel
		if err := m.ValidateChannel(*channel); err != nil {
			return err
		}
		// What it describes has to be the build it was told: binaries from an earlier run with another build number
		// would make every hub refuse the release after downloading it (#4522).
		if err := update.VerifyRelease(context.Background(), *dir, m); err != nil {
			return err
		}
		b, _ := json.MarshalIndent(m, "", "  ")
		return os.WriteFile(*out, append(b, '\n'), 0o644)
	case "verify":
		// The check before an upload, of the latest.json as written: publish.sh runs it on what it is about to put up.
		fs := flag.NewFlagSet("release verify", flag.ContinueOnError)
		dir := fs.String("dir", "", "where latest.json's downloads are")
		manifest := fs.String("manifest", "", "the manifest (default: latest.json in --dir)")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if err := noReleaseArgs("release verify", fs.Args()); err != nil {
			return err
		}
		if *dir == "" {
			return usageError("release verify needs --dir\n" + releaseUsage())
		}
		if *manifest == "" {
			*manifest = filepath.Join(*dir, "latest.json")
		}
		b, err := os.ReadFile(*manifest)
		if err != nil {
			return err
		}
		m, err := update.ParseManifest(b)
		if err != nil {
			return err
		}
		if err := m.ValidateEdition(update.ThisEdition); err != nil {
			return err
		}
		if err := update.VerifyRelease(context.Background(), *dir, m); err != nil {
			return err
		}
		fmt.Printf("%s describes the downloads in %s: %s, build %d\n", *manifest, *dir, m.Version, m.Build)
		return nil
	case "check-next":
		fs := flag.NewFlagSet("release check-next", flag.ContinueOnError)
		num := fs.String("build", "", "this release's build number")
		commit := fs.String("commit", "", "this release's commit")
		if len(args) >= 2 && (args[1] == "-h" || args[1] == "-help" || args[1] == "--help") {
			return fs.Parse(args[1:]) // prints the help; main exits 0 on flag.ErrHelp
		}
		if len(args) < 2 {
			return usageError("usage: " + commandName() + " release check-next <served latest.json> --build N --commit C")
		}
		served := args[1]
		if err := fs.Parse(args[2:]); err != nil {
			return err
		}
		if err := noReleaseArgs("release check-next", fs.Args()); err != nil {
			return err
		}
		n, err := update.ParseNumber(*num)
		if err != nil {
			return err
		}
		// Nothing served is a missing or empty file (publish.sh writes an empty one when the fetch fails). Any other
		// read error fails the check: a nil manifest passes it whatever the build and commit.
		var m *update.Manifest
		b, err := os.ReadFile(served)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("read the served manifest: %w", err)
		}
		if len(strings.TrimSpace(string(b))) > 0 {
			if m, err = update.ParseManifest(b); err != nil {
				return err
			}
		}
		return update.CheckNext(m, n, *commit, func(older, newer string) (bool, error) {
			err := exec.Command("git", "merge-base", "--is-ancestor", older, newer).Run()
			var exit *exec.ExitError
			if errors.As(err, &exit) && exit.ExitCode() == 1 {
				return false, nil
			}
			return err == nil, err
		})
	}
	return usageError(fmt.Sprintf("unknown release command %q\n%s", args[0], releaseUsage()))
}

// noReleaseArgs refuses what is left after a release subcommand's flags: flag parsing stops at the first argument
// that is not a flag, so a misplaced or mistyped one would drop every flag after it.
func noReleaseArgs(cmd string, rest []string) error {
	if len(rest) > 0 {
		return usageError(fmt.Sprintf("%s: unexpected argument %q\n%s", cmd, rest[0], releaseUsage()))
	}
	return nil
}

// releaseUsage is what `release` without a known subcommand prints.
func releaseUsage() string {
	return "usage: " + commandName() + " release manifest --dir D --version V --build N --commit C --out latest.json […]\n" +
		"       " + commandName() + " release verify --dir D [--manifest latest.json]\n" +
		"       " + commandName() + " release check-next <served latest.json> --build N --commit C\n" +
		"(" + commandName() + " release manifest -h for its flags)"
}
