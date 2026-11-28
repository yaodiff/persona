// Package chatexport exports one hub conversation for people working from a terminal.
package chatexport

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"time"

	"github.com/nwverse/persona/server/internal/clitext"
	"github.com/nwverse/persona/server/internal/exportfile"
	"github.com/nwverse/persona/server/internal/hubclient"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

const Usage = "  personad chat export …     export a private Markdown conversation (personad chat export -h)"

var (
	errStrict  = errors.New("fresh transcript read is unavailable")
	errInvalid = errors.New("incomplete transcript")
	errChanged = errors.New("conversation changed")
	errBudget  = errors.New("export limit reached")
)

func Run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	lang := i18n.Terminal()
	usage := i18n.T(lang, "chatexport.usage", i18n.Args{"command": clitext.Named("personad chat")})
	if len(args) == 1 && (clitext.IsHelpArg(args[0]) || args[0] == "help") {
		_, err := fmt.Fprintln(stdout, usage)
		return err
	}
	if len(args) == 0 || args[0] != "export" {
		return clitext.UsageError(usage)
	}
	flags := flag.NewFlagSet("export", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	hub := flags.String("hub", "", "")
	tokenFile := flags.String("token-file", "", "")
	output := flags.String("output", "", "")
	work := flags.Bool("include-steps", false, "")
	if err := flags.Parse(args[1:]); errors.Is(err, flag.ErrHelp) {
		_, err = fmt.Fprintln(stdout, usage)
		return err
	} else if err != nil || flags.NArg() != 2 || *tokenFile == "" || *output == "" || *output == "-" {
		return clitext.UsageError(usage)
	}
	machine, session := flags.Arg(0), flags.Arg(1)
	if !hubclient.ValidID(machine) || !hubclient.ValidID(session) {
		return clitext.UsageError(i18n.T(lang, "chatexport.invalid_id"))
	}
	base, err := hubclient.BaseURL(*hub)
	if err != nil {
		return clitext.UsageError(i18n.T(lang, "chatexport.invalid_hub"))
	}
	token, err := hubclient.Token(*tokenFile, "")
	if err != nil {
		return errors.New(i18n.T(lang, "chatexport.token"))
	}
	dest, err := exportfile.Open(*output)
	if err != nil {
		return exportError(lang, err)
	}
	defer dest.Close()

	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	fmt.Fprintln(stderr, i18n.T(lang, "chatexport.reading"))
	h := hubclient.New(base, token, lang, 35*time.Second, "closeout.markdown.v1")
	var hello struct {
		Type     string   `json:"type"`
		Features []string `json:"features"`
	}
	if err := h.Hello(ctx, &hello); err != nil {
		return readError(ctx, lang, err)
	}
	if hello.Type != "hello" || !slices.Contains(hello.Features, "sessions_lookup") {
		return errors.New(i18n.T(lang, "chatexport.hub_version"))
	}

	var document conversation
	for attempt := 0; attempt < 2; attempt++ {
		reader := snapshotReader{hub: h, machine: machine, remaining: maxReadBytes}
		document, err = reader.read(ctx, session, *work)
		if !errors.Is(err, errChanged) || attempt == 1 {
			break
		}
		fmt.Fprintln(stderr, i18n.T(lang, "chatexport.retrying"))
	}
	if err != nil {
		return readError(ctx, lang, err)
	}

	err = dest.Publish(ctx, func(out io.Writer) error {
		return writeMarkdown(out, lang, document, *work, time.Now().UTC())
	})
	if err != nil {
		return exportError(lang, err)
	}
	_, err = fmt.Fprintln(stdout, i18n.T(lang, "chatexport.saved", i18n.Args{"path": clitext.Inert(*output)}))
	return err
}

func readError(ctx context.Context, lang i18n.Lang, err error) error {
	var httpErr *hubclient.HTTPError
	switch {
	case ctx.Err() != nil:
		return exportError(lang, ctx.Err())
	case errors.As(err, &httpErr):
		if httpErr.Code == proto.CodeNodeOutdated {
			return errors.New(i18n.T(lang, "chatexport.node_version"))
		}
		// A transcript response can echo content. Print only the status, never its body or URL.
		return errors.New(i18n.T(lang, "chatexport.http_error", i18n.Args{"status": httpErr.Status}))
	case errors.Is(err, errStrict):
		return errors.New(i18n.T(lang, "chatexport.node_version"))
	case errors.Is(err, errChanged):
		return errors.New(i18n.T(lang, "chatexport.changed"))
	case errors.Is(err, errBudget):
		return errors.New(i18n.T(lang, "chatexport.limit"))
	case errors.Is(err, errMissing):
		return errors.New(i18n.T(lang, "chatexport.missing"))
	case errors.Is(err, hubclient.ErrTransport):
		return errors.New(i18n.T(lang, "chatexport.read_failed"))
	default:
		return errors.New(i18n.T(lang, "chatexport.incomplete"))
	}
}

func (r *snapshotReader) get(ctx context.Context, path string, query url.Values, out any) error {
	if r.remaining <= 0 {
		return errBudget
	}
	limit := min(maxReplyBytes, r.remaining)
	n, err := r.hub.JSON(ctx, http.MethodGet, path, query, limit, out)
	r.remaining -= n
	if n > limit {
		return errBudget
	}
	return err
}
