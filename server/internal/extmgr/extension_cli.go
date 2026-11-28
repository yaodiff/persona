package extmgr

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"golang.org/x/term"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/clitext"
	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/identity"
	"github.com/nwverse/persona/server/internal/managerconf"
)

// `personad extension …` on the hub's machine (docs/extensions/extensions.md "Command line"): what installs, upgrades and removes
// extensions. It talks to the running hub over loopback with the master token in hub.json, like `hub admin`.
// `personad ext …` is the same, and so is `personad plugin …` (its name before #1844).

const extensionUsage = `usage: personad extension <command> (also: personad ext, personad plugin)
  list                              the extensions and their state
  show <id>                         one extension: permissions, settings, secrets set or not
  install <source> [--yes]          install (source: a directory, dir:/p, file:/p.tar.gz | .zip,
                                    https://…/p.tar.gz#sha256=<hex>, remote:rpc=/p.sock,http=/q.sock);
                                    shows what it may do and asks first unless --yes
  upgrade <id> <source> [--yes]     a newer version of an installed extension
  rollback <id>                     back to the version before the last upgrade
  attach <id> remote:rpc=/p.sock,http=/q.sock
                                    reach an installed package's program, running on its own at the same version, on
                                    its sockets; the package, settings, secrets and data stay
  detach <id>                       run the attached package's program here again
  enable <id> | disable <id> [--cascade]
                                    --cascade: the extensions that depend on it are switched off too
  sandbox <id> on|off|default       run it in a sandbox or not (default: on unless from a trusted market)
  auth <id> [login|logout]          an extension's sign-in (manifest.auth): its state; login shows the link, code and QR
                                    code here and waits until it is done; logout signs it out
  config <id> key=value …           set settings (a value is JSON when it parses, text otherwise)
  secret <id> <name>                set a secret, read from stdin (never an argument)
  secret <id> <name> --delete
  uninstall <id> [--keep-data] [--cascade] [--members]
                                    --members: a pack's members go with it
  market list                       the extension markets this hub reads
  market add <spec> [--token] [--key=ed25519:…]
                                    a market: https://example.com/extensions/ (its persona-extensions.json),
                                    a .json URL, github:owner/repo[@ref] or
                                    git:<address>[#branch]; --token reads one from stdin, --token-from=<market id>
                                    uses that registered market's token (never shown); --key (repeatable) is a public
                                    key its index must be signed with (a private market, see sign-index)
  market set-spec <id> <spec>       the market moved (a renamed repository): same id, so what was installed from it
                                    stays its own
  market remove <id> | refresh <id>
  market token <id> [--clear]       set the market's token (read from stdin) and read it again; --clear removes it
  market key list <id>              a private market's trusted public keys
  market key add <id> <ed25519:…>   trust one more key (rotation: add the new one, re-sign, then remove the old one)
  market key remove <id> <key id>   stop trusting a key; with none left the market is unsigned again
  search [words]                    the markets' extensions
  install market:<market>/<id>      install (or upgrade) one of a market's extensions
  auto-upgrade on|off [<id>]        market extensions upgrade themselves (all of them, or one)
  upgrades                          newer versions: going in, waiting for your confirmation, skipped
  audit [<id>] [--limit=N]          who installed, switched, signed in, deleted what, and when, newest first
                                    (an extension, a connector, feishu for the Feishu bot; all of them without an id)
  Writes that can change an extension (install, upgrade, rollback, attach/detach,
  enable/disable, sandbox, auth login/logout, config, secret, auto-upgrade,
  contribution, market writes, dev, uninstall) print the resolved home and hub
  first. They also take --expect-home <absolute path> and --expect-hub <host:port>;
  a mismatch stops before any preview, request or write.`

// ExtensionCommand runs `personad extension <args>`.
func ExtensionCommand(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return errors.New(clitext.Named(extensionUsage + extensionBuildUsage))
	}
	switch args[0] {
	case "new", "test", "pack":
		return extensionBuildCommand(ctx, args, stdout, stderr)
	case "keygen", "sign-index":
		return marketSignCommand(args, stdout)
	case "help", "-h", "--help":
		fmt.Fprintln(stdout, clitext.Named(extensionUsage+extensionBuildUsage))
		return nil
	}
	// Before hub.json is read or the hub is called: a mistyped option must not run the command without it.
	inv, err := checkExtensionArgs(args)
	if err != nil {
		return err
	}
	l := i18n.Terminal()
	cfg, err := localHubConfig(l)
	if err != nil {
		return err
	}
	c := &extensionClient{cfg: cfg, lang: l}
	flag := func(name string) bool {
		return inv.hasOption(name)
	}
	positional := inv.positional // as many as extensionCommandArgs allows
	if inv.writes {
		if err := checkExtensionTarget(l, stdout, c, inv); err != nil {
			return err
		}
	}
	switch args[0] {
	case "dev":
		return c.dev(ctx, dirArg(positional), stdout, stderr)
	case "list":
		var out struct {
			Extensions []ExtensionView `json:"extensions"`
			Legacy     []ExtensionView `json:"plugins"` // a hub before #1844
		}
		if err := c.do(ctx, "GET", "/api/v1/extensions", nil, &out); err != nil {
			return err
		}
		out.Extensions = append(out.Extensions, out.Legacy...)
		if len(out.Extensions) == 0 {
			fmt.Fprintln(stdout, i18n.T(l, "extmgr-cli.list_empty"))
		}
		for _, p := range out.Extensions {
			line := fmt.Sprintf("%-20s %-10s %-9s %-12s %s", p.ID, p.Version, p.Source, p.Status, p.Name)
			if p.Error != "" {
				line += i18n.T(l, "extmgr-cli.paren", i18n.Args{"text": p.Error})
			}
			if len(p.NeedsSetup) > 0 {
				line += i18n.T(l, "extmgr-cli.list_needs_setup", i18n.Args{"list": cliList(l, p.NeedsSetup)})
			}
			line += attachedLine(l, p.Attached, p.Error == "")
			fmt.Fprintln(stdout, strings.TrimRight(line, " "))
		}
		return nil
	case "show":
		var d ExtensionDetail
		if err := c.do(ctx, "GET", "/api/v1/extensions/"+url.PathEscape(positional[0])+"/-/", nil, &d); err != nil {
			return err
		}
		b, _ := json.MarshalIndent(d, "", "  ")
		fmt.Fprintln(stdout, string(b))
		return nil
	case "install", "upgrade":
		upgrade, source := "", ""
		if args[0] == "upgrade" {
			upgrade, source = positional[0], positional[1]
		} else {
			source = positional[0]
		}
		var pv extension.Preview
		if rest, ok := strings.CutPrefix(source, "market:"); ok {
			market, id, ok := strings.Cut(rest, "/")
			if !ok {
				return errors.New(i18n.T(l, "extmgr-cli.market_source"))
			}
			if err := c.do(ctx, "POST", "/api/v1/extensions/-/catalog/preview", map[string]string{"market": market, "extension": id, "plugin": id}, &pv); err != nil {
				return err
			}
			if pv.Upgrade {
				upgrade = pv.Manifest.ID
				if pv.RenameFrom != "" {
					upgrade = pv.RenameFrom
				}
			}
		} else if err := c.do(ctx, "POST", "/api/v1/extensions/-/preview", map[string]string{"source": source, "upgrade": upgrade}, &pv); err != nil {
			return err
		}
		printPreview(l, stdout, &pv)
		printBundle(l, stdout, &pv)
		if !flag("--yes") && !confirm(l, stdin, stdout) {
			return errors.New(i18n.T(l, "extmgr-cli.install_declined"))
		}
		var d ExtensionDetail
		body := map[string]any{"preview_id": pv.ID, "permissions_digest": pv.Digest, "bundle": bundleRefs(&pv)}
		path := "/api/v1/extensions/-/install"
		if upgrade != "" {
			path = "/api/v1/extensions/" + url.PathEscape(upgrade) + "/-/upgrade"
		}
		if err := c.do(ctx, "POST", path, body, &d); err != nil {
			return err
		}
		fmt.Fprintln(stdout, i18n.T(l, "extmgr-cli.installed", i18n.Args{"id": d.ID, "version": d.Version, "status": d.Status}))
		if len(d.NeedsSetup) > 0 {
			fmt.Fprintln(stdout, i18n.T(l, "extmgr-cli.needs_setup", i18n.Args{"list": cliList(l, d.NeedsSetup)}))
		}
		return nil
	case "market":
		switch positional[0] {
		case "list":
			var out struct {
				Markets []MarketView `json:"markets"`
			}
			if err := c.do(ctx, "GET", "/api/v1/extensions/-/markets", nil, &out); err != nil {
				return err
			}
			if len(out.Markets) == 0 {
				fmt.Fprintln(stdout, i18n.T(l, "extmgr-cli.markets_empty"))
			}
			for _, m := range out.Markets {
				line := fmt.Sprintf("%-10s %-24s %s  %-12s %s", m.ID, m.Name,
					i18n.T(l, "extmgr-cli.extension_count", i18n.Args{"count": m.Extensions}), signatureLabel(l, m.Signature), m.Spec)
				if m.Official {
					line += "  " + i18n.T(l, "extmgr-cli.market_official")
				}
				if m.Error != "" {
					line += i18n.T(l, "extmgr-cli.paren", i18n.Args{"text": m.Error})
				}
				fmt.Fprintln(stdout, line)
			}
			return nil
		case "add":
			body := map[string]string{"spec": positional[1]}
			if flag("--token") {
				tok, err := readSecret(l, stdin, stdout)
				if err != nil {
					return err
				}
				body["token"] = tok
			}
			var keys []string
			for _, a := range args[1:] {
				if from, ok := strings.CutPrefix(a, "--token-from="); ok {
					body["token_from"] = from
				}
				if key, ok := strings.CutPrefix(a, "--key="); ok {
					keys = append(keys, key)
				}
			}
			var m MarketView
			var in any = body
			if len(keys) > 0 {
				withKeys := map[string]any{"keys": keys}
				for k, v := range body {
					withKeys[k] = v
				}
				in = withKeys
			}
			if err := c.do(ctx, "POST", "/api/v1/extensions/-/markets", in, &m); err != nil {
				return err
			}
			fmt.Fprintln(stdout, i18n.T(l, "extmgr-cli.market", i18n.Args{"id": m.ID, "name": m.Name, "count": m.Extensions}))
			for _, w := range m.Warnings {
				fmt.Fprintln(stdout, "  "+i18n.T(l, "extmgr-cli.warning", i18n.Args{"text": w}))
			}
			return nil
		case "set-spec":
			var m MarketView
			if err := c.do(ctx, "PUT", "/api/v1/extensions/-/markets/"+url.PathEscape(positional[1]), map[string]string{"spec": positional[2]}, &m); err != nil {
				return err
			}
			fmt.Fprintln(stdout, i18n.T(l, "extmgr-cli.market_spec", i18n.Args{"id": m.ID, "name": m.Name, "count": m.Extensions, "spec": m.Spec}))
			return nil
		case "token":
			tok := ""
			if !flag("--clear") {
				var err error
				if tok, err = readSecret(l, stdin, stdout); err != nil {
					return err
				}
				if strings.TrimSpace(tok) == "" {
					return errors.New(i18n.T(l, "extmgr-cli.token_empty"))
				}
			}
			var m MarketView
			if err := c.do(ctx, "PUT", "/api/v1/extensions/-/markets/"+url.PathEscape(positional[1])+"/token", map[string]string{"token": tok}, &m); err != nil {
				return err
			}
			args := i18n.Args{"id": m.ID, "name": m.Name, "count": m.Extensions}
			line := i18n.T(l, "extmgr-cli.market_token_cleared", args)
			if m.HasToken {
				line = i18n.T(l, "extmgr-cli.market_token_set", args)
			}
			if m.Error != "" {
				line += i18n.T(l, "extmgr-cli.paren", i18n.Args{"text": m.Error})
			}
			fmt.Fprintln(stdout, line)
			return nil
		case "key":
			return marketKeyCommand(ctx, c, l, positional[1:], stdout)
		case "remove", "refresh":
			if positional[0] == "remove" {
				return c.do(ctx, "DELETE", "/api/v1/extensions/-/markets/"+url.PathEscape(positional[1]), nil, nil)
			}
			var m MarketView
			if err := c.do(ctx, "POST", "/api/v1/extensions/-/markets/"+url.PathEscape(positional[1])+"/refresh", nil, &m); err != nil {
				return err
			}
			fmt.Fprintln(stdout, i18n.T(l, "extmgr-cli.market", i18n.Args{"id": m.ID, "name": m.Name, "count": m.Extensions}))
			return nil
		}
		return errors.New(clitext.Named(extensionUsage))
	case "search":
		var out struct {
			Extensions []CatalogItem `json:"extensions"`
			Legacy     []CatalogItem `json:"plugins"` // a hub before #1844
		}
		if err := c.do(ctx, "GET", "/api/v1/extensions/-/catalog?q="+url.QueryEscape(strings.Join(positional, " ")), nil, &out); err != nil {
			return err
		}
		out.Extensions = append(out.Extensions, out.Legacy...)
		if len(out.Extensions) == 0 {
			fmt.Fprintln(stdout, i18n.T(l, "extmgr-cli.search_empty"))
		}
		for _, p := range out.Extensions {
			state := ""
			switch {
			case p.Upgradable:
				state = i18n.T(l, "extmgr-cli.search_upgradable", i18n.Args{"version": p.Installed})
			case p.Installed != "":
				state = i18n.T(l, "extmgr-cli.search_installed", i18n.Args{"version": p.Installed})
			case p.Problem != "":
				state = i18n.T(l, "extmgr-cli.paren", i18n.Args{"text": p.Problem})
			}
			fmt.Fprintf(stdout, "market:%s/%s  %s %s%s\n    %s\n", p.Market, p.ID, p.Name, p.Version, state, p.Description)
		}
		return nil
	case "auto-upgrade":
		if positional[0] != "on" && positional[0] != "off" {
			return errors.New(clitext.Named(extensionUsage))
		}
		on := positional[0] == "on"
		if len(positional) > 1 {
			var d ExtensionDetail
			if err := c.do(ctx, "PUT", "/api/v1/extensions/"+url.PathEscape(positional[1])+"/-/auto-upgrade", map[string]bool{"auto": on}, &d); err != nil {
				return err
			}
			key := "extmgr-cli.auto_upgrade_one_off"
			if on {
				key = "extmgr-cli.auto_upgrade_one_on"
			}
			fmt.Fprintln(stdout, i18n.T(l, key, i18n.Args{"id": d.ID}))
			return nil
		}
		var v upgradesView
		if err := c.do(ctx, "PUT", "/api/v1/extensions/-/upgrades", map[string]bool{"auto": on}, &v); err != nil {
			return err
		}
		fmt.Fprintln(stdout, autoUpgradeLine(l, v.Auto))
		if v.Auto && len(v.Off) > 0 {
			fmt.Fprintln(stdout, i18n.T(l, "extmgr-cli.auto_upgrade_off_ones", i18n.Args{"list": cliList(l, v.Off)}))
		}
		return nil
	case "audit": // docs/extensions/extensions.md, after the install steps
		return auditCommand(ctx, c, args[1:], stdout)
	case "upgrades":
		var v upgradesView
		if err := c.do(ctx, "GET", "/api/v1/extensions/-/upgrades", nil, &v); err != nil {
			return err
		}
		printUpgrades(l, stdout, v)
		return nil
	case "rollback":
		var d ExtensionDetail
		if err := c.do(ctx, "POST", "/api/v1/extensions/"+url.PathEscape(positional[0])+"/-/rollback", map[string]string{}, &d); err != nil {
			return err
		}
		fmt.Fprintln(stdout, i18n.T(l, "extmgr-cli.rolled_back", i18n.Args{"id": d.ID, "version": d.Version}))
		return nil
	case "attach", "detach":
		body := map[string]string{}
		if args[0] == "attach" {
			body["source"] = positional[1]
		}
		var d ExtensionDetail
		if err := c.do(ctx, "POST", "/api/v1/extensions/"+url.PathEscape(positional[0])+"/-/"+args[0], body, &d); err != nil {
			return err
		}
		fmt.Fprintln(stdout, idStatus(l, d))
		return nil
	case "enable", "disable":
		var d ExtensionDetail
		body := map[string]bool{"enabled": args[0] == "enable", "cascade": flag("--cascade")}
		if err := c.do(ctx, "PUT", "/api/v1/extensions/"+url.PathEscape(positional[0])+"/-/enabled", body, &d); err != nil {
			return err
		}
		fmt.Fprintln(stdout, idStatus(l, d))
		return nil
	case "auth": // docs/extensions/extensions.md "Sign-in (auth)"
		return extensionAuthCommand(ctx, c, positional, stdin, stdout)
	case "sandbox": // docs/extensions/extensions.md "Sandbox"
		var on *bool
		switch positional[1] {
		case "on", "off":
			v := positional[1] == "on"
			on = &v
		case "default":
		default:
			return errors.New("personad extension sandbox <id> on|off|default")
		}
		var d ExtensionDetail
		if err := c.do(ctx, "PUT", "/api/v1/extensions/"+url.PathEscape(positional[0])+"/-/sandbox", map[string]*bool{"enabled": on}, &d); err != nil {
			return err
		}
		switch sb := d.Sandbox; {
		case sb == nil:
			fmt.Fprintln(stdout, i18n.T(l, "extmgr-cli.sandbox_none", i18n.Args{"id": d.ID}))
		case sb.Enabled && sb.Active:
			fmt.Fprintln(stdout, i18n.T(l, "extmgr-cli.sandbox_active", i18n.Args{"id": d.ID}))
		case sb.Enabled:
			fmt.Fprintln(stdout, i18n.T(l, "extmgr-cli.sandbox_blocked", i18n.Args{"id": d.ID, "why": sb.Why}))
		default:
			fmt.Fprintln(stdout, i18n.T(l, "extmgr-cli.sandbox_off", i18n.Args{"id": d.ID}))
		}
		return nil
	case "config":
		id := positional[0]
		var d ExtensionDetail
		if err := c.do(ctx, "GET", "/api/v1/extensions/"+url.PathEscape(id)+"/-/", nil, &d); err != nil {
			return err
		}
		cfg := map[string]json.RawMessage{}
		_ = json.Unmarshal(d.Config, &cfg)
		for _, kv := range positional[1:] {
			k, v, ok := strings.Cut(kv, "=")
			if !ok {
				return fmt.Errorf("%q is not key=value", kv)
			}
			if v == "" {
				delete(cfg, k)
				continue
			}
			if json.Valid([]byte(v)) {
				cfg[k] = json.RawMessage(v)
			} else {
				cfg[k], _ = json.Marshal(v)
			}
		}
		if err := c.do(ctx, "PUT", "/api/v1/extensions/"+url.PathEscape(id)+"/-/config", map[string]any{"config": cfg}, &d); err != nil {
			return err
		}
		fmt.Fprintln(stdout, idStatus(l, d))
		return nil
	case "secret":
		path := "/api/v1/extensions/" + url.PathEscape(positional[0]) + "/-/secrets/" + url.PathEscape(positional[1])
		var d ExtensionDetail
		if flag("--delete") {
			if err := c.do(ctx, "DELETE", path, nil, &d); err != nil {
				return err
			}
			fmt.Fprintln(stdout, i18n.T(l, "extmgr-cli.secret_deleted", i18n.Args{"id": d.ID, "name": positional[1]}))
			return nil
		}
		value, err := readSecret(l, stdin, stdout)
		if err != nil {
			return err
		}
		if strings.TrimSpace(value) == "" { // the hub would refuse it with a size error
			return errors.New(i18n.T(l, "extmgr-cli.secret_empty"))
		}
		if err := c.do(ctx, "PUT", path, map[string]string{"value": value}, &d); err != nil {
			return err
		}
		fmt.Fprintln(stdout, idStatus(l, d))
		return nil
	case "uninstall":
		q := url.Values{}
		for f, k := range map[string]string{"--keep-data": "keep_data", "--cascade": "cascade", "--members": "members"} {
			if flag(f) {
				q.Set(k, "1")
			}
		}
		path := "/api/v1/extensions/" + url.PathEscape(positional[0]) + "/-/"
		if len(q) > 0 {
			path += "?" + q.Encode()
		}
		var out struct {
			Warnings []string `json:"warnings"`
		}
		if err := c.do(ctx, "DELETE", path, nil, &out); err != nil {
			return err
		}
		fmt.Fprintln(stdout, i18n.T(l, "extmgr-cli.uninstalled", i18n.Args{"id": positional[0]}))
		for _, warning := range out.Warnings {
			fmt.Fprintln(stderr, i18n.T(l, "extmgr-cli.warning", i18n.Args{"text": warning}))
		}
		return nil
	}
	if done, err := extensionModelCommand(ctx, c, args[0], positional, stdout); done {
		return err
	}
	return unknownExtensionCommand(args[0])
}

func unknownExtensionCommand(name string) error {
	return fmt.Errorf("unknown extension command %q\n%s\n%s", name, clitext.Named(extensionUsage), extensionModelUsage)
}

// extensionInvocation is a parsed hub command line.
type extensionInvocation struct {
	positional []string
	options    []string
	values     map[string]string
	writes     bool
}

func (i extensionInvocation) hasOption(name string) bool {
	return slices.Contains(i.options, name)
}

func (i extensionInvocation) value(name string) (string, bool) {
	value, ok := i.values[name]
	return value, ok
}

// extensionArgs is what a hub command takes (extensionUsage, docs/extensions/extensions.md "Command line").
type extensionArgs struct {
	min, max     int      // positional arguments (a market command's include its own word); max -1: any number
	options      []string // exact words; one ending in "=" takes a value after it (--token-from=<id>)
	valueOptions []string // options that take a value as the next word or after "="
	writes       bool     // the command can change hub state
	writesWhen   func([]string) bool
	exclusive    bool // at most one of the options
	// repeatable: options (ending in "=") that may be given several times and do not count for exclusive.
	repeatable []string
	usage      string // printed with a mistake; extensionUsage when empty
}

var extensionTargetOptions = []string{"--expect-home", "--expect-hub"}

// extensionWrite is a command that changes hub state: it gets the target options and reports the target before dispatch.
func extensionWrite(min, max int, options ...string) extensionArgs {
	return extensionArgs{min: min, max: max, options: options, valueOptions: extensionTargetOptions, writes: true}
}

// extensionCommandArgs are the hub commands and their arguments; `audit` parses its own, and new / test / pack need
// no hub.
var extensionCommandArgs = map[string]extensionArgs{
	"list":            {},
	"upgrades":        {},
	"show":            {min: 1, max: 1},
	"rollback":        extensionWrite(1, 1),
	"attach":          extensionWrite(2, 2),
	"detach":          extensionWrite(1, 1),
	"install":         extensionWrite(1, 1, "--yes"),
	"upgrade":         extensionWrite(2, 2, "--yes"),
	"enable":          extensionWrite(1, 1, "--cascade"),
	"disable":         extensionWrite(1, 1, "--cascade"),
	"sandbox":         extensionWrite(2, 2),
	"auth":            {min: 1, max: 2, valueOptions: extensionTargetOptions, writesWhen: extensionAuthWrites},
	"config":          extensionWrite(2, -1),
	"secret":          extensionWrite(2, 2, "--delete"),
	"uninstall":       extensionWrite(1, 1, "--keep-data", "--cascade", "--members"),
	"search":          {max: -1},
	"auto-upgrade":    extensionWrite(1, 2),
	"dev":             extensionWrite(0, 1),
	"contributions":   {min: 1, max: 1, usage: extensionModelUsage},
	"contribution":    {min: 3, max: 3, valueOptions: extensionTargetOptions, writes: true, usage: extensionModelUsage},
	"market list":     {min: 1, max: 1},
	"market add":      {min: 2, max: 2, options: []string{"--token", "--token-from="}, valueOptions: extensionTargetOptions, writes: true, exclusive: true, repeatable: []string{"--key="}},
	"market key":      {min: 3, max: 4, valueOptions: extensionTargetOptions, writesWhen: extensionMarketKeyWrites},
	"market set-spec": extensionWrite(3, 3),
	"market remove":   extensionWrite(2, 2),
	"market refresh":  extensionWrite(2, 2),
	"market token":    {min: 2, max: 2, options: []string{"--clear"}, valueOptions: extensionTargetOptions, writes: true},
}

// isExtensionOption is a word that is not a positional argument.
func isExtensionOption(a string) bool { return len(a) > 1 && strings.HasPrefix(a, "-") }

// checkExtensionArgs parses a hub command line and refuses one extensionCommandArgs does not allow: an unknown
// command, an option the command does not take, a missing or an extra argument.
func checkExtensionArgs(args []string) (extensionInvocation, error) {
	inv := extensionInvocation{values: map[string]string{}}
	if args[0] == "audit" {
		inv.positional = append(inv.positional, args[1:]...)
		return inv, nil
	}
	name := args[0]
	if name == "market" {
		sub, err := extensionMarketSubcommand(args[1:])
		if err != nil {
			return inv, err
		}
		name += " " + sub
	}
	spec, ok := extensionCommandArgs[name]
	if !ok {
		return inv, unknownExtensionCommand(name)
	}
	usage := clitext.Named(extensionUsage)
	if spec.usage != "" {
		usage = spec.usage
	}
	mistake := func(format string, a ...any) error {
		return fmt.Errorf("%s: %s\n%s", clitext.Named("personad extension "+name), fmt.Sprintf(format, a...), usage)
	}
	for i := 1; i < len(args); i++ {
		a := args[i]
		if !isExtensionOption(a) {
			inv.positional = append(inv.positional, a)
			continue
		}
		if base, value, hasValue := strings.Cut(a, "="); hasValue && slices.Contains(spec.valueOptions, base) {
			if value == "" {
				return inv, mistake("%s needs a value", base)
			}
			if _, exists := inv.values[base]; exists {
				return inv, mistake("%s is given more than once", base)
			}
			inv.values[base] = value
			continue
		}
		if slices.Contains(spec.valueOptions, a) {
			if i+1 >= len(args) || isExtensionOption(args[i+1]) {
				return inv, mistake("%s needs a value", a)
			}
			if _, exists := inv.values[a]; exists {
				return inv, mistake("%s is given more than once", a)
			}
			inv.values[a] = args[i+1]
			i++
			continue
		}
		known := func(want string) bool { return a == want || strings.HasSuffix(want, "=") && strings.HasPrefix(a, want) }
		switch {
		case strings.HasSuffix(a, "=") && (slices.Contains(spec.options, a) || slices.Contains(spec.repeatable, a)): // --token-from= with nothing after it
			return inv, mistake("%s needs a value", a)
		case slices.ContainsFunc(spec.repeatable, known):
			continue
		case !slices.ContainsFunc(spec.options, known):
			return inv, mistake("unknown option %q", a)
		}
		inv.options = append(inv.options, a)
		if spec.exclusive && a != inv.options[0] {
			return inv, mistake("%q and %q do not go together", inv.options[0], a)
		}
	}
	inv.writes = spec.writes
	if spec.writesWhen != nil {
		inv.writes = spec.writesWhen(inv.positional)
	}
	if len(inv.positional) < spec.min {
		return inv, errors.New(usage)
	}
	if spec.max >= 0 && len(inv.positional) > spec.max {
		return inv, mistake("unexpected argument %q", inv.positional[spec.max])
	}
	return inv, nil
}

// extensionMarketSubcommand is the first positional word after market. The market subcommand is known before its
// own options are parsed, so skip options here; in particular, never mistake a target option's value for it.
func extensionMarketSubcommand(args []string) (string, error) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if isExtensionOption(a) {
			if !strings.Contains(a, "=") && slices.Contains(extensionTargetOptions, a) {
				i++
			}
			continue
		}
		return a, nil
	}
	return "", errors.New(clitext.Named(extensionUsage))
}

func extensionAuthWrites(positional []string) bool {
	return len(positional) > 1 && (positional[1] == "login" || positional[1] == "logout")
}

func extensionMarketKeyWrites(positional []string) bool {
	return len(positional) > 1 && (positional[1] == "add" || positional[1] == "remove")
}

// checkExtensionTarget prints the home and loopback hub a write will use, then checks explicit expectations. Nothing
// here touches the hub or the filesystem beyond the hub.json localHubConfig already read.
func checkExtensionTarget(l i18n.Lang, w io.Writer, c *extensionClient, inv extensionInvocation) error {
	home := filepath.Clean(c.cfg.dir)
	hub := managerconf.CLIHubURL(c.cfg.Listen)
	wantHome, hasHome := inv.value("--expect-home")
	wantHub, hasHub := inv.value("--expect-hub")
	if hasHome && !filepath.IsAbs(wantHome) {
		return errors.New(i18n.T(l, "extmgr-cli.expect_home_absolute", i18n.Args{"path": wantHome}))
	}
	if hasHub {
		var err error
		if wantHub, err = expectedHub(wantHub); err != nil {
			return errors.New(i18n.T(l, "extmgr-cli.expect_hub_address", i18n.Args{"value": inv.values["--expect-hub"]}))
		}
	}
	fmt.Fprintln(w, i18n.T(l, "extmgr-cli.target_home", i18n.Args{"home": home}))
	fmt.Fprintln(w, i18n.T(l, "extmgr-cli.target_hub", i18n.Args{"hub": hub}))
	if hasHome && home != filepath.Clean(wantHome) {
		return errors.New(i18n.T(l, "extmgr-cli.target_home_mismatch", i18n.Args{
			"actual": home, "expected": filepath.Clean(wantHome),
		}))
	}
	if hasHub && !sameHub(hub, wantHub) {
		return errors.New(i18n.T(l, "extmgr-cli.target_hub_mismatch", i18n.Args{
			"actual": hub, "expected": wantHub,
		}))
	}
	return nil
}

func expectedHub(value string) (string, error) {
	host, port, err := net.SplitHostPort(value)
	if err != nil || host == "" {
		return "", errors.New("host:port")
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return "", errors.New("host:port")
	}
	return net.JoinHostPort(host, port), nil
}

func sameHub(actual, expected string) bool {
	u, err := url.Parse(actual)
	if err != nil {
		return false
	}
	actualHost, actualPort, err := net.SplitHostPort(u.Host)
	if err != nil {
		return false
	}
	expectedHost, expectedPort, err := net.SplitHostPort(expected)
	if err != nil {
		return false
	}
	return strings.EqualFold(actualHost, expectedHost) && actualPort == expectedPort
}

// printPreview is what an install or upgrade will do, in l (the hub wrote the permission lines and warnings in the
// language the command asked for, extensionClient.call).
func printPreview(l i18n.Lang, w io.Writer, pv *extension.Preview) {
	m := pv.Manifest
	if pv.RenameFrom != "" {
		fmt.Fprintln(w, i18n.T(l, "extension-identity.confirmation", i18n.Args{"from": pv.RenameFrom, "to": m.ID}))
	}
	if pv.Upgrade {
		fmt.Fprintln(w, i18n.T(l, "extmgr-cli.preview_upgrade", i18n.Args{"id": m.ID, "from": pv.From, "to": m.Version}))
	} else {
		fmt.Fprintln(w, i18n.T(l, "extmgr-cli.preview_install", i18n.Args{"id": m.ID, "version": m.Version, "name": m.Name}))
	}
	if m.Publisher != "" {
		fmt.Fprintln(w, i18n.T(l, "extmgr-cli.preview_publisher", i18n.Args{"publisher": m.Publisher}))
	}
	if m.Description != "" {
		fmt.Fprintln(w, m.Description)
	}
	for point, ids := range m.Contributions() {
		fmt.Fprintln(w, i18n.T(l, "extmgr-cli.preview_provides", i18n.Args{"point": point, "list": cliList(l, ids)}))
	}
	fmt.Fprintln(w, i18n.T(l, "extmgr-cli.preview_can"))
	if len(pv.Permissions) == 0 {
		fmt.Fprintln(w, "  "+i18n.T(l, "extmgr-cli.preview_no_program"))
	}
	for _, p := range pv.Permissions {
		fmt.Fprintln(w, "  · "+i18n.T(l, "extmgr-cli.preview_permission", i18n.Args{"title": p.Title, "detail": p.Detail}))
	}
	if pv.MorePermissions {
		fmt.Fprintln(w, i18n.T(l, "extmgr-cli.preview_more_permissions"))
	}
	for _, warn := range pv.Warnings {
		fmt.Fprintln(w, i18n.T(l, "extmgr-cli.preview_note", i18n.Args{"text": warn}))
	}
}

func confirm(l i18n.Lang, stdin io.Reader, stdout io.Writer) bool {
	fmt.Fprint(stdout, i18n.T(l, "extmgr-cli.confirm")+" [y/N] ")
	line, _ := bufio.NewReader(stdin).ReadString('\n')
	line = strings.ToLower(strings.TrimSpace(line))
	return line == "y" || line == "yes"
}

// cliList joins a command line's list in l.
func cliList(l i18n.Lang, items []string) string {
	return strings.Join(items, i18n.T(l, "extmgr-cli.list_separator"))
}

// idStatus is an extension's state after a command changed it: its ID and status word.
func idStatus(l i18n.Lang, d ExtensionDetail) string {
	return i18n.T(l, "extmgr-cli.id_status", i18n.Args{"id": d.ID, "status": d.Status})
}

// autoUpgradeLine is whether market extensions upgrade themselves.
func autoUpgradeLine(l i18n.Lang, on bool) string {
	if on {
		return i18n.T(l, "extmgr-cli.auto_upgrade_on")
	}
	return i18n.T(l, "extmgr-cli.auto_upgrade_off")
}

// attachedLine is what `extension list` adds for an attached extension: online or offline and since when, why it is
// offline when the line does not say it yet, and the version the program says it is when that was not approved.
func attachedLine(l i18n.Lang, a *ExtensionAttached, withError bool) string {
	if a == nil {
		return ""
	}
	line := i18n.T(l, "extmgr-cli.attached_offline")
	if a.Online {
		line = i18n.T(l, "extmgr-cli.attached_online")
	}
	if !a.Since.IsZero() {
		line += i18n.T(l, "extmgr-cli.attached_since", i18n.Args{"time": a.Since.Format("01-02 15:04:05")})
	}
	if withError && a.Error != "" {
		line += i18n.T(l, "extmgr-cli.attached_error", i18n.Args{"error": a.Error})
	}
	if a.Version != "" {
		line += i18n.T(l, "extmgr-cli.attached_version", i18n.Args{"version": a.Version})
	}
	return line
}

// readSecret reads a secret from the terminal without echo, or the first line of a pipe.
func readSecret(l i18n.Lang, stdin io.Reader, stdout io.Writer) (string, error) {
	if f, ok := stdin.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		fmt.Fprint(stdout, i18n.T(l, "extmgr-cli.secret_prompt")+" ")
		b, err := term.ReadPassword(int(f.Fd()))
		fmt.Fprintln(stdout)
		return strings.TrimSpace(string(b)), err
	}
	line, err := bufio.NewReader(stdin).ReadString('\n')
	if err != nil && err != io.EOF {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

// localHubConfig is hub.json on this machine (the token, the port).
func localHubConfig(l i18n.Lang) (*Config, error) {
	dir := identity.HomeDir()
	cfg := &Config{Listen: ":18660", dir: dir}
	b, err := os.ReadFile(dir + "/hub.json")
	if err != nil {
		return nil, errors.New(i18n.T(l, "extmgr-cli.no_hub_config", i18n.Args{"dir": dir}))
	}
	if err := json.Unmarshal(b, cfg); err != nil {
		return nil, err
	}
	if v := identity.Getenv("PERSONA_LISTEN"); v != "" {
		cfg.Listen = v
	}
	return cfg, nil
}

// extensionClient calls the hub for a command; lang is the terminal's (i18n.Terminal), what the command prints in
// and asks the hub to write its text in.
type extensionClient struct {
	cfg  *Config
	lang i18n.Lang
}

// l is the client's language: an unset one is English, as i18n.Terminal's is without a locale, so what the client
// words itself and what it asks the hub for always agree.
func (c *extensionClient) l() i18n.Lang { return c.lang.Or(i18n.En) }

// do calls the hub on this machine from loopback (managerconf.CLIHubURL, CLIDial) with the master token.
func (c *extensionClient) do(ctx context.Context, method, path string, body, out any) error {
	err := c.call(ctx, method, path, body, out)
	if legacy, ok := strings.CutPrefix(path, "/api/v1/extensions"); ok && errors.Is(err, errNoRoute) {
		// A hub before #1844 (not restarted since this binary came): the old paths.
		err = c.call(ctx, method, "/api/v1/plugins"+legacy, body, out)
	}
	if errors.Is(err, errNoRoute) {
		return errors.New(i18n.T(c.l(), "extmgr-cli.hub_too_old"))
	}
	return err
}

// errNoRoute is a 404 that is not the hub's JSON answer: the hub has no such route.
var errNoRoute = errors.New("no such route")

func (c *extensionClient) call(ctx context.Context, method, path string, body, out any) error {
	_, port, err := net.SplitHostPort(c.cfg.Listen)
	if n, perr := strconv.Atoi(port); err != nil || perr != nil || n <= 0 || n > 65535 {
		return errors.New("hub.json listen has no valid port")
	}
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, managerconf.CLIHubURL(c.cfg.Listen)+path, r)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.cfg.Token)
	req.Header.Set("Content-Type", "application/json")
	// The hub's text (errors, permission lines, statuses it words) comes in the terminal's language, not the user
	// language an app last set (#5520).
	req.Header.Set("Accept-Language", string(c.l()))
	transport := &http.Transport{Proxy: nil, DialContext: managerconf.CLIDial}
	defer transport.CloseIdleConnections()
	client := &http.Client{Timeout: 10 * time.Minute, Transport: transport} // a download may take a while
	res, err := client.Do(req)
	if err != nil {
		var ne *net.OpError
		if errors.As(err, &ne) {
			return errors.New(i18n.T(c.l(), "extmgr-cli.hub_not_running"))
		}
		return err
	}
	defer res.Body.Close()
	data, err := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	if err != nil {
		return err
	}
	if res.StatusCode == http.StatusNotFound && !json.Valid(data) {
		return errNoRoute
	}
	if res.StatusCode != http.StatusOK {
		why := apierr.Describe(data) // with the cause the hub moved to detail (#5728)
		if why == "" {
			why = strings.TrimSpace(string(data))
		}
		return errors.New(i18n.T(c.l(), "extmgr-cli.http_error", i18n.Args{"error": why, "status": res.StatusCode}))
	}
	if out != nil {
		return json.Unmarshal(data, out)
	}
	return nil
}

// printUpgrades lists the newer versions by what happens to them.
func printUpgrades(l i18n.Lang, w io.Writer, v upgradesView) {
	fmt.Fprintln(w, autoUpgradeLine(l, v.Auto))
	groups := []struct {
		title  string
		states []string
	}{
		{"extmgr-cli.upgrades_available", []string{upgradeAvailable, upgradeWaitingIdle, upgradeOff, upgradeFailed, upgradeIncompatible}},
		{"extmgr-cli.upgrades_needs_consent", []string{upgradeNeedsConsent}},
		{"extmgr-cli.upgrades_skipped", []string{upgradeSkipped}},
	}
	what := map[string]string{upgradeAvailable: "extmgr-cli.upgrade_state_available", upgradeWaitingIdle: "extmgr-cli.upgrade_state_waiting_idle",
		upgradeOff: "extmgr-cli.upgrade_state_off", upgradeFailed: "extmgr-cli.upgrade_state_failed",
		upgradeIncompatible: "extmgr-cli.upgrade_state_incompatible", upgradeSkipped: "extmgr-cli.upgrade_state_skipped"}
	any := false
	for _, g := range groups {
		var lines []string
		for _, up := range v.Upgrades {
			if !slices.Contains(g.states, up.State) {
				continue
			}
			line := fmt.Sprintf("  %-20s %s → %s", up.ID, up.From, up.To)
			switch {
			case up.State == upgradeNeedsConsent:
				// A renamed extension is in its market under its new ID (#2511).
				line += "  " + i18n.T(l, "extmgr-cli.upgrade_consent", i18n.Args{"detail": upgradeConsentText(l, up),
					"command": clitext.Named("personad extension install market:" + up.Market + "/" + cmp.Or(up.RenameTo, up.ID))})
			case up.Detail != "":
				line += "  " + i18n.T(l, "extmgr-cli.upgrade_state_detail", i18n.Args{"state": i18n.T(l, what[up.State]), "detail": up.Detail})
			default:
				line += "  " + i18n.T(l, what[up.State])
			}
			lines = append(lines, line)
		}
		if len(lines) > 0 {
			any = true
			fmt.Fprintln(w, i18n.T(l, g.title))
			for _, line := range lines {
				fmt.Fprintln(w, line)
			}
		}
	}
	if !any {
		fmt.Fprintln(w, i18n.T(l, "extmgr-cli.upgrades_none"))
	}
}

// signatureLabel is a market's signature in a line of the command line.
func signatureLabel(l i18n.Lang, s MarketSignatureView) string {
	switch s.State {
	case extension.SigSigned:
		return i18n.T(l, "extmgr-cli.signature_signed", i18n.Args{"key": s.KeyID})
	case extension.SigInvalid:
		return i18n.T(l, "extmgr-cli.signature_invalid")
	case extension.SigKeyChanged:
		return i18n.T(l, "extmgr-cli.signature_key_changed", i18n.Args{"key": s.KeyID})
	}
	return i18n.T(l, "extmgr-cli.signature_unsigned")
}
