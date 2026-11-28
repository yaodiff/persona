package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/nwverse/persona/server/internal/hubclient"
	"github.com/nwverse/persona/server/internal/teamdocs"
)

const memberDocumentBytes = 256 << 10

// A JSON escape can occupy six bytes for one byte of ASCII text.
const memberReplyBytes = 6*memberDocumentBytes + 4096

var errMemberReceipt = errors.New("project service returned an invalid revision receipt")

type memberClient struct {
	base  *url.URL
	token string
	http  *http.Client
}

func newMemberClient(baseURL, token string) (*memberClient, error) {
	base, err := hubclient.BaseURL(baseURL)
	if err != nil {
		return nil, errors.New("set the project service URL to HTTPS or local HTTP")
	}
	credential, err := base64.RawURLEncoding.Strict().DecodeString(token)
	if err != nil || len(credential) != 32 || len(token) != base64.RawURLEncoding.EncodedLen(32) {
		return nil, errors.New("invalid project member token")
	}
	return &memberClient{base: base, token: token, http: &http.Client{
		Timeout:       30 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}, nil
}

func validMemberID(s string) bool {
	if len(s) == 0 || len(s) > 100 {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}

func validMemberDigest(s string) bool {
	if len(s) != sha256.Size*2 || strings.ToLower(s) != s {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}

func validMemberContent(s string) bool {
	return len(s) > 0 && len(s) <= memberDocumentBytes && utf8.ValidString(s) && !strings.ContainsRune(s, 0)
}

func (c *memberClient) publish(ctx context.Context, project, document string, expected int64, content string) (teamdocs.Revision, error) {
	if expected < 0 || expected == math.MaxInt64 {
		return teamdocs.Revision{}, errors.New("invalid expected revision")
	}
	if !validMemberContent(content) {
		return teamdocs.Revision{}, errors.New("document must contain 1 to 262144 bytes of UTF-8 text without NUL bytes")
	}
	body := struct {
		Expected int64  `json:"expected_revision"`
		Content  string `json:"content"`
	}{expected, content}
	rev, err := c.request(ctx, project, document, expected+1, "revisions", body, http.StatusCreated)
	if err != nil {
		return teamdocs.Revision{}, err
	}
	if rev.Content != content || rev.ApprovedBy != "" {
		return teamdocs.Revision{}, errMemberReceipt
	}
	return rev, nil
}

func (c *memberClient) read(ctx context.Context, project, document string, revision int64) (teamdocs.Revision, error) {
	if revision < 1 {
		return teamdocs.Revision{}, errors.New("specify a positive revision number")
	}
	return c.request(ctx, project, document, revision, "revisions/"+strconv.FormatInt(revision, 10), nil, http.StatusOK)
}

func (c *memberClient) approve(ctx context.Context, project, document string, revision int64, digest string) (teamdocs.Revision, error) {
	if !validMemberDigest(digest) {
		return teamdocs.Revision{}, errors.New("specify the reviewed SHA-256 as 64 lowercase hexadecimal digits")
	}
	reviewed, err := c.read(ctx, project, document, revision)
	if err != nil {
		return teamdocs.Revision{}, err
	}
	if reviewed.SHA256 != digest {
		return teamdocs.Revision{}, errors.New("the revision differs from the reviewed SHA-256")
	}
	// Even an already-approved revision must go through the service's head and role checks.
	body := struct {
		Revision int64 `json:"revision"`
	}{revision}
	rev, err := c.request(ctx, project, document, revision, "approvals", body, http.StatusOK)
	if err != nil {
		return teamdocs.Revision{}, err
	}
	if rev.SHA256 != digest || rev.Author != reviewed.Author || rev.ApprovedAt == nil ||
		(reviewed.ApprovedAt != nil && (rev.ApprovedBy != reviewed.ApprovedBy || !rev.ApprovedAt.Equal(*reviewed.ApprovedAt))) {
		return teamdocs.Revision{}, errMemberReceipt
	}
	return rev, nil
}

func (c *memberClient) request(ctx context.Context, project, document string, number int64, path string, payload any, want int) (teamdocs.Revision, error) {
	if !validMemberID(project) || !validMemberID(document) {
		return teamdocs.Revision{}, errors.New("invalid project or document ID")
	}
	method := http.MethodGet
	var body io.Reader
	if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			return teamdocs.Revision{}, errors.New("cannot encode the project document request")
		}
		body, method = bytes.NewReader(raw), http.MethodPost
	}
	endpoint := c.base.JoinPath("v1", "projects", project, "documents", document, path)
	req, err := http.NewRequestWithContext(ctx, method, endpoint.String(), body)
	if err != nil {
		return teamdocs.Revision{}, errors.New("invalid project service request")
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.token)
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return teamdocs.Revision{}, errors.New("could not reach the project service; check the revision before retrying a write")
	}
	defer resp.Body.Close()
	if resp.StatusCode != want {
		return teamdocs.Revision{}, memberStatusError(resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, memberReplyBytes+1))
	if err != nil || len(raw) > memberReplyBytes {
		return teamdocs.Revision{}, errMemberReceipt
	}
	var rev teamdocs.Revision
	if err := json.Unmarshal(raw, &rev); err != nil || rev.Project != project || rev.Document != document || rev.Number != number ||
		!validMemberID(rev.Author) || !validMemberContent(rev.Content) || (rev.ApprovedAt == nil) != (rev.ApprovedBy == "") ||
		(rev.ApprovedAt != nil && (!validMemberID(rev.ApprovedBy) || rev.ApprovedAt.IsZero())) {
		return teamdocs.Revision{}, errMemberReceipt
	}
	sum := sha256.Sum256([]byte(rev.Content))
	if rev.SHA256 != hex.EncodeToString(sum[:]) {
		return teamdocs.Revision{}, errMemberReceipt
	}
	return rev, nil
}

func memberStatusError(status int) error {
	switch status {
	case http.StatusUnauthorized:
		return errors.New("project member credential was rejected (HTTP 401)")
	case http.StatusForbidden:
		return errors.New("project member role does not permit this action (HTTP 403)")
	case http.StatusNotFound:
		return errors.New("project document or revision was not found (HTTP 404)")
	case http.StatusConflict:
		return errors.New("document head changed; review the current revision before retrying (HTTP 409)")
	default:
		return fmt.Errorf("unexpected project service response (HTTP %d)", status)
	}
}
