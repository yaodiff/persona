// Package recordingcli lists and exports recordings through the paired-device knowledge base API.
//
//persona:layer application
package recordingcli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/clitext"
	"github.com/nwverse/persona/server/internal/exportfile"
	"github.com/nwverse/persona/server/internal/hubclient"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/kbwire"
	"github.com/nwverse/persona/server/internal/recordingwire"
)

const Usage = "  personad recordings list|export  find recordings and save notes or captions (personad recordings -h)"

const (
	tokenEnv = "PERSONA_RECORDINGS_TOKEN"
	listPath = "/api/v1/kb/recordings"
	// A detail can contain the stored text as both structured segments and Markdown, with JSON escaping.
	maxReplyBytes = 64 << 20
)

// Run reads retained recordings, including on a hub where the recording module is off.
func Run(ctx context.Context, args []string, stdout io.Writer) error {
	lang := i18n.Terminal()
	usage := i18n.T(lang, "recordingcli.usage", i18n.Args{"command": clitext.Named("personad recordings")})
	if len(args) == 0 {
		return clitext.UsageError(usage)
	}
	command := args[0]
	if command == "help" || clitext.IsHelpArg(command) {
		_, err := fmt.Fprintln(stdout, usage)
		return err
	}
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	hub := flags.String("hub", "", "")
	tokenFile := flags.String("token-file", "", "")
	var query, format, path string
	var asJSON bool
	switch command {
	case "list":
		flags.StringVar(&query, "query", "", "")
		flags.BoolVar(&asJSON, "json", false, "")
	case "export":
		flags.StringVar(&format, "format", "markdown", "")
		flags.Func("output", "", func(value string) error {
			if value == "" {
				return errors.New("empty output path")
			}
			path = value
			return nil
		})
	default:
		return clitext.UsageError(usage)
	}
	if err := flags.Parse(args[1:]); errors.Is(err, flag.ErrHelp) {
		_, err = fmt.Fprintln(stdout, usage)
		return err
	} else if err != nil {
		return clitext.UsageError(usage)
	}
	var id recordingwire.ID
	if command == "export" {
		if flags.NArg() != 1 {
			return clitext.UsageError(usage)
		}
		var valid bool
		if id, valid = recordingwire.ParseID(flags.Arg(0)); !valid {
			return clitext.UsageError(i18n.T(lang, "recordingcli.invalid_id"))
		}
		if format != "markdown" && format != "vtt" {
			return clitext.UsageError(i18n.T(lang, "recordingcli.invalid_format"))
		}
	} else if flags.NArg() != 0 {
		return clitext.UsageError(usage)
	}
	base, err := hubclient.BaseURL(*hub)
	if err != nil {
		return clitext.UsageError(i18n.T(lang, "outbox.invalid_hub"))
	}
	token, err := hubclient.Token(*tokenFile, tokenEnv)
	if err != nil {
		return errors.New(i18n.T(lang, "recordingcli.token"))
	}
	c := client{hub: hubclient.New(base, token, lang, 30*time.Second), lang: lang}
	if command == "list" {
		return c.list(ctx, stdout, strings.TrimSpace(query), asJSON)
	}
	return c.export(ctx, stdout, id, format, path)
}

type client struct {
	hub  *hubclient.Client
	lang i18n.Lang
}

func (c client) get(ctx context.Context, path string, query url.Values, out any) error {
	_, err := c.hub.JSON(ctx, http.MethodGet, path, query, maxReplyBytes, out)
	var failure *hubclient.HTTPError
	switch {
	case err == nil:
		return nil
	case ctx.Err() != nil:
		return ctx.Err()
	case errors.As(err, &failure):
		return errors.New(i18n.T(c.lang, "recordingcli.http_error", i18n.Args{"status": failure.Status}))
	case errors.Is(err, hubclient.ErrTransport):
		return errors.New(i18n.T(c.lang, "recordingcli.read_failed"))
	default:
		return c.badResponse()
	}
}

func (c client) badResponse() error {
	return errors.New(i18n.T(c.lang, "recordingcli.bad_response"))
}

type recordingList struct {
	Items []kbwire.Entry `json:"items"`
}

func (c client) list(ctx context.Context, stdout io.Writer, query string, asJSON bool) error {
	var list recordingList
	if query == "" {
		if err := c.get(ctx, listPath, nil, &list); err != nil {
			return err
		}
	} else {
		var matches struct {
			Results []struct {
				Entry kbwire.Entry `json:"entry"`
			} `json:"results"`
		}
		values := url.Values{"q": {query}, "kind": {recordingwire.KindRecording}, "results": {"1"}}
		if err := c.get(ctx, "/api/v1/kb-search", values, &matches); err != nil {
			return err
		}
		if matches.Results == nil {
			return c.badResponse()
		}
		list.Items = make([]kbwire.Entry, 0, len(matches.Results))
		for _, match := range matches.Results {
			// Older hubs ignored kind and returned entries of every kind.
			if match.Entry.Kind == "" {
				return c.badResponse()
			}
			if match.Entry.Kind == recordingwire.KindRecording {
				list.Items = append(list.Items, match.Entry)
			}
		}
	}
	if list.Items == nil {
		return c.badResponse()
	}
	for _, item := range list.Items {
		id, ok := recordingwire.RecordingID(item.ID)
		if !ok || id != item.ID || item.Kind != recordingwire.KindRecording {
			return c.badResponse()
		}
	}
	if asJSON {
		return json.NewEncoder(stdout).Encode(list)
	}
	if len(list.Items) == 0 {
		_, err := fmt.Fprintln(stdout, i18n.T(c.lang, "recordingcli.empty"))
		return err
	}
	for _, item := range list.Items {
		name := clitext.Inert(item.Name)
		if name == "" {
			name = i18n.T(c.lang, "recordingcli.untitled")
		}
		if _, err := fmt.Fprintf(stdout, "%s\t%s\n", item.ID, name); err != nil {
			return err
		}
	}
	return nil
}

func (c client) export(ctx context.Context, stdout io.Writer, id recordingwire.ID, format, path string) error {
	var dest *exportfile.Destination
	if path != "" {
		var err error
		dest, err = exportfile.Open(path)
		if err != nil {
			return c.fileError(err)
		}
		defer dest.Close()
	}
	var result struct {
		Detail kbwire.Detail `json:"detail"`
	}
	if err := c.get(ctx, listPath+"/"+string(id), nil, &result); err != nil {
		return err
	}
	if result.Detail.Kind != recordingwire.KindRecording || result.Detail.ID != string(id) {
		return c.badResponse()
	}
	body, err := renderRecording(result.Detail, format, c.lang)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	write := func(out io.Writer) error {
		n, err := out.Write(body)
		if n < len(body) && err == nil {
			return io.ErrShortWrite
		}
		return err
	}
	if dest == nil {
		return write(stdout)
	}
	if err := dest.Publish(ctx, write); err != nil {
		return c.fileError(err)
	}
	_, err = fmt.Fprintln(stdout, i18n.T(c.lang, "recordingcli.saved", i18n.Args{"path": clitext.Inert(path)}))
	return err
}

func (c client) fileError(err error) error {
	if errors.Is(err, exportfile.ErrExists) {
		return errors.New(i18n.T(c.lang, "chatexport.exists"))
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return errors.New(i18n.T(c.lang, "recordingcli.output_failed"))
}
