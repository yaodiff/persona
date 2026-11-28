package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"math"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/nwverse/persona/server/internal/teamdocs"
)

type memberOptions struct {
	service, tokenFile string
	project, document  string
	file, digest       string
	number             int64
}

func parseMemberCommand(args []string) (memberOptions, error) {
	var o memberOptions
	if len(args) == 0 {
		return o, errors.New(usage)
	}
	f := flag.NewFlagSet(args[0], flag.ContinueOnError)
	f.SetOutput(io.Discard)
	f.StringVar(&o.service, "url", os.Getenv("TEAM_PROJECT_URL"), "team document service URL")
	f.StringVar(&o.tokenFile, "token-file", os.Getenv("TEAM_PROJECT_TOKEN_FILE"), "project member token file")
	var number string
	switch args[0] {
	case "publish":
		f.StringVar(&number, "expected", "", "expected head revision (0 creates a document)")
		f.StringVar(&o.file, "file", "", "UTF-8 document file")
	case "read", "approve":
		f.StringVar(&number, "revision", "", "revision number")
		if args[0] == "approve" {
			f.StringVar(&o.digest, "sha256", "", "reviewed SHA-256")
		}
	default:
		return o, errors.New(usage)
	}
	if err := f.Parse(args[1:]); err != nil || f.NArg() != 2 {
		return o, errors.New(usage)
	}
	o.project, o.document = f.Arg(0), f.Arg(1)
	if !validMemberID(o.project) || !validMemberID(o.document) {
		return o, errors.New("invalid project or document ID")
	}
	n, err := strconv.ParseUint(number, 10, 63)
	if err != nil || args[0] == "publish" && n == math.MaxInt64 || args[0] != "publish" && n == 0 {
		if args[0] == "publish" {
			return o, errors.New("set -expected to the current revision number (0 for a new document)")
		}
		return o, errors.New("set -revision to a positive revision number")
	}
	o.number = int64(n)
	if args[0] == "publish" && o.file == "" {
		return o, errors.New("set -file to the document to publish")
	}
	if args[0] == "approve" && !validMemberDigest(o.digest) {
		return o, errors.New("set -sha256 to the reviewed revision's lowercase SHA-256 digest")
	}
	if o.service == "" || o.tokenFile == "" {
		return o, errors.New("set -url and -token-file or TEAM_PROJECT_URL and TEAM_PROJECT_TOKEN_FILE")
	}
	return o, nil
}

// readMemberFile bounds both the file on disk and the bytes read if it grows. Keep paths and
// contents out of errors: a misconfigured token or document can accidentally be passed as a path.
func readMemberFile(path string, limit int) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > int64(limit) {
		return nil, errors.New("invalid file")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, errors.New("cannot open file")
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, int64(limit)+1))
	if err != nil || len(data) == 0 || len(data) > limit {
		return nil, errors.New("cannot read file")
	}
	return data, nil
}

func runMemberCommand(ctx context.Context, args []string, out io.Writer) error {
	o, err := parseMemberCommand(args)
	if err != nil {
		return err
	}
	token, err := readMemberFile(o.tokenFile, 4096)
	if err != nil {
		return errors.New("cannot read the project token file")
	}
	client, err := newMemberClient(o.service, strings.TrimSuffix(strings.TrimSuffix(string(token), "\n"), "\r"))
	if err != nil {
		return err
	}
	var revision teamdocs.Revision
	switch args[0] {
	case "publish":
		body, err := readMemberFile(o.file, memberDocumentBytes)
		if err != nil || !utf8.Valid(body) || bytes.IndexByte(body, 0) >= 0 {
			return errors.New("provide a nonempty UTF-8 document file of at most 256 KiB without NUL bytes")
		}
		revision, err = client.publish(ctx, o.project, o.document, o.number, string(body))
		if err != nil {
			return err
		}
	case "read":
		revision, err = client.read(ctx, o.project, o.document, o.number)
		if err != nil {
			return err
		}
		_, err = io.WriteString(out, revision.Content)
		return err
	case "approve":
		revision, err = client.approve(ctx, o.project, o.document, o.number, o.digest)
		if err != nil {
			return err
		}
	}
	// Receipts can be retained in scripts and logs. The document body belongs only to read.
	return json.NewEncoder(out).Encode(struct {
		Project    string     `json:"project"`
		Document   string     `json:"document"`
		Revision   int64      `json:"revision"`
		SHA256     string     `json:"sha256"`
		Author     string     `json:"author"`
		ApprovedBy string     `json:"approved_by,omitempty"`
		ApprovedAt *time.Time `json:"approved_at,omitempty"`
	}{revision.Project, revision.Document, revision.Number, revision.SHA256, revision.Author, revision.ApprovedBy, revision.ApprovedAt})
}
