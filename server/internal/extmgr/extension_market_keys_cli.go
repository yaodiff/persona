package extmgr

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/nwverse/persona/server/internal/clitext"
	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/i18n"
)

// The command line of private market keys (docs/extensions/extensions.md "Private market keys"): `market key …` asks
// the hub; `keygen` and `sign-index` are a publisher's, offline, and never talk to a hub.

const marketKeyUsage = `usage: personad extension market key list <market id>
       personad extension market key add <market id> <ed25519:…>
       personad extension market key remove <market id> <key id | ed25519:…>`

const marketSignUsage = `usage: personad extension keygen --out <private key file>
       personad extension sign-index <persona-extensions.json> --key <private key file> [--key <file>…] [--out <file>]`

// marketKeyCommand runs `market key list|add|remove` (args: the word after "key" and its arguments).
func marketKeyCommand(ctx context.Context, c *extensionClient, l i18n.Lang, args []string, stdout io.Writer) error {
	if len(args) < 2 {
		return errors.New(clitext.Named(marketKeyUsage))
	}
	base := "/api/v1/extensions/-/markets/" + url.PathEscape(args[1]) + "/keys"
	var m MarketView
	switch {
	case args[0] == "list" && len(args) == 2:
		var out marketKeysOut
		if err := c.do(ctx, "GET", base, nil, &out); err != nil {
			return err
		}
		if len(out.Keys) == 0 {
			fmt.Fprintln(stdout, i18n.T(l, "extmgr-cli.market_keys_none"))
		}
		for _, id := range slices.Sorted(maps.Keys(out.Keys)) {
			fmt.Fprintf(stdout, "%s  %s\n", id, out.Keys[id])
		}
		return nil
	case args[0] == "add" && len(args) == 3:
		if err := c.do(ctx, "POST", base, map[string]string{"key": args[2]}, &m); err != nil {
			return err
		}
	case args[0] == "remove" && len(args) == 3:
		if err := c.do(ctx, "DELETE", base+"/"+url.PathEscape(args[2]), nil, &m); err != nil {
			return err
		}
	default:
		return errors.New(clitext.Named(marketKeyUsage))
	}
	line := i18n.T(l, "extmgr-cli.market_keys", i18n.Args{"id": m.ID, "name": m.Name, "count": len(m.TrustedKeys)}) + "  " + signatureLabel(l, m.Signature)
	if m.Error != "" {
		line += i18n.T(l, "extmgr-cli.paren", i18n.Args{"text": m.Error})
	}
	fmt.Fprintln(stdout, line)
	return nil
}

// marketSignCommand runs keygen and sign-index.
func marketSignCommand(args []string, stdout io.Writer) error {
	var files []string
	var keys []string
	out := ""
	for i := 1; i < len(args); i++ {
		a := args[i]
		value := func() (string, error) {
			if _, v, ok := strings.Cut(a, "="); ok {
				return v, nil
			}
			if i+1 >= len(args) {
				return "", fmt.Errorf("%s needs a value\n%s", a, marketSignUsage)
			}
			i++
			return args[i], nil
		}
		switch name, _, _ := strings.Cut(a, "="); name {
		case "--key":
			v, err := value()
			if err != nil {
				return err
			}
			keys = append(keys, v)
		case "--out":
			v, err := value()
			if err != nil {
				return err
			}
			out = v
		default:
			if strings.HasPrefix(a, "-") {
				return fmt.Errorf("unknown option %q\n%s", a, clitext.Named(marketSignUsage))
			}
			files = append(files, a)
		}
	}
	if args[0] == "keygen" {
		if out == "" || len(files) > 0 || len(keys) > 0 {
			return errors.New(clitext.Named(marketSignUsage))
		}
		pub, err := writeMarketKey(out)
		if err != nil {
			return err
		}
		fmt.Fprintf(stdout, "%s\nkey id %s\n", extension.FormatSigningKey(pub), extension.KeyID(pub))
		return nil
	}
	if len(files) != 1 || len(keys) == 0 {
		return errors.New(clitext.Named(marketSignUsage))
	}
	privs := make([]ed25519.PrivateKey, 0, len(keys))
	for _, k := range keys {
		priv, err := readMarketKey(k)
		if err != nil {
			return err
		}
		privs = append(privs, priv)
	}
	raw, err := os.ReadFile(files[0])
	if err != nil {
		return err
	}
	payload, err := extension.CatalogPayload(raw)
	if err != nil {
		return fmt.Errorf("%s: %w", files[0], err)
	}
	signed, err := extension.SignCatalog(payload, privs...)
	if err != nil {
		return err
	}
	if out == "" {
		out = files[0]
	}
	if err := writeFileAtomic(out, append(signed, '\n'), 0o644); err != nil {
		return err
	}
	for _, p := range privs {
		fmt.Fprintf(stdout, "%s: signed by %s\n", out, extension.KeyID(p.Public().(ed25519.PublicKey)))
	}
	return nil
}

// writeMarketKey makes a new Ed25519 key and writes its private half to path (PKCS #8 PEM, the form openssl reads,
// mode 0600, never over an existing file); it returns the public half.
func writeMarketKey(path string) (ed25519.PublicKey, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, err
	}
	if err := pem.Encode(f, &pem.Block{Type: "PRIVATE KEY", Bytes: der}); err != nil {
		_ = f.Close()
		return nil, err
	}
	return pub, f.Close()
}

// readMarketKey reads an Ed25519 private key in PKCS #8 PEM (keygen's, or `openssl genpkey -algorithm ed25519`'s).
func readMarketKey(path string) (ed25519.PrivateKey, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(b)
	if block == nil || block.Type != "PRIVATE KEY" {
		return nil, fmt.Errorf("%s: not a PKCS #8 PEM private key", path)
	}
	k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	priv, ok := k.(ed25519.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("%s: not an Ed25519 key", path)
	}
	return priv, nil
}

// writeFileAtomic replaces path with data through a temporary file in the same directory.
func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".persona-sign-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
