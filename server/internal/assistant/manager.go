package assistant

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	neturl "net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/identity"
	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/link"
	"github.com/nwverse/persona/server/internal/managerconf"
	"github.com/nwverse/persona/server/internal/update"
)

// A manager made in the app (docs/assistant/managers.md, "A second manager") is the same program as the assistant the hub starts, with
// a directory of its own: manager.json there says who it is and where the hub is, and everything it keeps on disk (the
// knowledge base, secret.key, its extensions) is under it, never under the hub's. Its state is in a Postgres of its own,
// never the hub's: the container `personad manager install` makes for it (internal/manager), or the one the user gave.

// ManagerFile is a manager's configuration, in its directory (0600: it holds the manager's bus token).
const ManagerFile = "manager.json"

// ManagerConfig is manager.json.
type ManagerConfig struct {
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
	// Hub is the hub's base URL as this manager reaches it (http(s)://host:port), HubID the hub it joined.
	Hub   string `json:"hub"`
	HubID string `json:"hub_id,omitempty"`
	Token string `json:"token"`
	// Fingerprint pins the hub's TLS key on an https Hub with the hub's own certificate (link.Pin); "" trusts the
	// system's CAs.
	Fingerprint string `json:"fingerprint,omitempty"`
	// DatabaseURL is its own database: its container's (DatabaseContainer), or the one --database-url gave (made
	// when missing). Empty until `personad manager install` (or run) made its container.
	DatabaseURL string `json:"database_url,omitempty"`
	// DatabaseContainer is the Docker container its Postgres runs in when install made it (persona-mgr-<id>-pg); ""
	// for a database the user gave, which uninstall never touches.
	DatabaseContainer string `json:"database_container,omitempty"`
}

// managerID is the shape of a manager's id (the hub's busManagerID).
var managerID = lazyre.New(`^[a-z][a-z0-9-]{0,31}$`)

func (c *ManagerConfig) check() error {
	switch {
	case !managerID.MatchString(c.ID):
		return fmt.Errorf("manager id %q: lower-case letters, digits and -", c.ID)
	case c.ID == assistantwire.AssistantID:
		return errors.New("the hub runs the manager " + c.ID + " itself (personad assistant)")
	case !strings.HasPrefix(c.Hub, "http://") && !strings.HasPrefix(c.Hub, "https://"):
		return fmt.Errorf("hub %q: want http(s)://host:port", c.Hub)
	case strings.TrimSpace(c.Token) == "":
		return errors.New("no token")
	}
	return nil
}

// DefaultManagerDir is where `personad manager join` puts manager id when not told: managers/<id> in this machine's
// data directory (~/.persona/managers/<id>).
func DefaultManagerDir(id string) string { return filepath.Join(identity.HomeDir(), "managers", id) }

// ReadManagerConfig reads manager.json in dir.
func ReadManagerConfig(dir string) (*ManagerConfig, error) {
	b, err := os.ReadFile(filepath.Join(dir, ManagerFile))
	if err != nil {
		return nil, err
	}
	var c ManagerConfig
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("%s: %w", filepath.Join(dir, ManagerFile), err)
	}
	if err := c.check(); err != nil {
		return nil, fmt.Errorf("%s: %w", filepath.Join(dir, ManagerFile), err)
	}
	return &c, nil
}

// WriteManagerConfig writes manager.json in dir (made 0700 when missing), 0600, in one step: a manager running from
// it reads either the old one or the new one. Only who it is changes: its sections of the configuration and anything
// else in the file stay as they are.
func WriteManagerConfig(dir string, c ManagerConfig) error {
	return WriteManagerConfigTo(dir, c, os.Stderr)
}

// WriteManagerConfigTo is WriteManagerConfig, saying on warn where it kept a manager.json that did not read.
func WriteManagerConfigTo(dir string, c ManagerConfig, warn io.Writer) error {
	if err := c.check(); err != nil {
		return err
	}
	b, err := json.Marshal(c)
	if err != nil {
		return err
	}
	who, err := managerconf.ParseObject(b)
	if err != nil {
		return err
	}
	path := filepath.Join(dir, ManagerFile)
	var aside string
	var why error
	// One that does not read (cut short, edited wrong) is kept beside it rather than refused: the install code that
	// gave this identity is spent already. Said, since its sections (the gate rules among them) are not in the new one.
	if b, err := os.ReadFile(path); err == nil {
		if _, err := managerconf.ParseObject(b); err != nil || len(bytes.TrimSpace(b)) == 0 {
			if err == nil {
				err = errors.New("empty")
			}
			aside = path + ".unreadable-" + time.Now().UTC().Format("20060102T150405Z")
			if err := os.Rename(path, aside); err != nil {
				return err
			}
			why = err
		}
	}
	err = managerconf.Edit(path, func(o *managerconf.Object) error {
		for _, k := range managerIdentity {
			if v, ok := who.Get(k); ok {
				o.SetRaw(k, v)
			} else {
				o.Del(k)
			}
		}
		return nil
	})
	if aside != "" {
		if err != nil {
			return fmt.Errorf("%w (the one that did not read is kept as %s)", err, aside)
		}
		fmt.Fprintf(warn, "manager: %s did not read (%v): kept as %s, and a new one written with only who the "+
			"manager is; copy its sections (gate rules, connectors, extensions, bots) back into it\n", path, why, aside)
	}
	return err
}

// managerIdentity are ManagerConfig's keys in manager.json.
var managerIdentity = []string{"id", "name", "hub", "hub_id", "token", "fingerprint", "database_url", "database_container"}

// HubDatabaseURL is the database_url of the hub on this machine (hub.json in the data directory,
// PERSONA_DATABASE_URL over it); "" when there is none. A manager never uses it (SameDatabase).
func HubDatabaseURL() string {
	cfg, err := readHubConfig()
	if err != nil {
		return ""
	}
	return cfg.DatabaseURL
}

// ErrHubDatabase: the database a manager was given is the hub's own.
var ErrHubDatabase = errors.New("this is the hub's own database: a manager keeps its state in a Postgres of its own")

// SameDatabase: a and b name the same database on the same server, as far as their URLs tell: host names compared
// without case, every loopback name (localhost, 127.0.0.0/8, ::1) as one, a missing port as 5432.
func SameDatabase(a, b string) bool {
	server := func(raw string) string {
		u, err := neturl.Parse(raw)
		if err != nil {
			return raw
		}
		host, port := strings.ToLower(u.Hostname()), u.Port()
		if port == "" {
			port = "5432"
		}
		if ip := net.ParseIP(host); host == "localhost" || (ip != nil && ip.IsLoopback()) {
			host = "loopback"
		}
		return host + ":" + port
	}
	return server(a) == server(b) && db.DatabaseName(a) == db.DatabaseName(b)
}

// RunManager is `personad manager run`: the manager whose manager.json is in dir, until ctx ends. Its data
// directory is dir (PERSONA_HOME too, for what it starts); its database is manager.json's database_url (made when it
// is missing on that server), which the caller made sure there is (manager.ownDatabase). When the hub refuses its
// token (removed in the app, or installed again elsewhere) it says so once and returns bus.ErrRevoked, not trying
// again with it.
func RunManager(ctx context.Context, build update.Build, dir string) error {
	return RunManagerWith(ctx, build, dir, MainHooks{})
}

// RunManagerWith is RunManager with h.
func RunManagerWith(ctx context.Context, build update.Build, dir string, h MainHooks) error {
	logger := log.New(os.Stderr, "manager: ", log.LstdFlags)
	dir, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	c, err := ReadManagerConfig(dir)
	if err != nil {
		return err
	}
	// Its sections of the configuration are in its manager.json, beside who it is; a hub.json of its directory (an
	// older build read the sections there) moves in once. One that does not read stops it before it opens its
	// database (its service starts it again, and it says why again).
	file := filepath.Join(dir, ManagerFile)
	ext, err := moveConfig(dir, file, nil, logger.Printf)
	if err != nil {
		return err
	}
	url := c.DatabaseURL
	if url == "" {
		return errors.New("no database: run personad manager install, which makes its own Postgres in Docker, or give --database-url")
	}
	if created, err := db.EnsureDatabase(ctx, url); err != nil {
		return fmt.Errorf("database %s: %w", db.DatabaseName(url), err)
	} else if created {
		logger.Printf("[%s] made its database %s", c.ID, db.DatabaseName(url))
	}
	hostDataDir := h.HostDataDir
	if hostDataDir == "" {
		hostDataDir = identity.HomeDir()
	}
	// What it starts (extensions, tools) finds its data directory, not the hub's.
	_ = identity.Setenv("PERSONA_HOME", dir)
	deploy := identity.Getenv("PERSONA_ASSISTANT_DEPLOY")
	if deploy == "" {
		deploy = "external"
		if _, err := os.Stat("/.dockerenv"); err == nil {
			deploy = "docker"
		}
	}
	return run(ctx, build, runOptions{ID: c.ID, Hub: c.Hub, Token: c.Token, Fingerprint: c.Fingerprint, Deploy: deploy,
		Dir: dir, HostDataDir: hostDataDir, ConfigFile: file, ExtConfig: ext, DatabaseURL: url, StopWhenRevoked: true,
		Logf: logger.Printf, Ready: h.Ready, PendingRestart: h.PendingRestart, HubVersion: h.HubVersion})
}

// pinnedHTTPClient dials the hub only when its TLS key is fp's.
func pinnedHTTPClient(fp string) *http.Client { return link.Pinned(fp) }
