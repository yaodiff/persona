package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMemberCommandOptions(t *testing.T) {
	t.Setenv("TEAM_PROJECT_URL", "https://team.example")
	t.Setenv("TEAM_PROJECT_TOKEN_FILE", "reader.token")
	digest := strings.Repeat("a", 64)
	for _, args := range [][]string{
		{"publish", "-file", "notes", "team", "plan"},
		{"publish", "-expected", "0", "team", "plan"},
		{"publish", "-file", "notes", "-expected", "9223372036854775807", "team", "plan"},
		{"read", "-revision", "0", "team", "plan"},
		{"read", "-revision", "0x10", "team", "plan"},
		{"read", "-revision", "9223372036854775808", "team", "plan"},
		{"read", "-revision", "1", "../other", "plan"},
		{"read", "-revision", "1", "team", "plan", "extra"},
		{"approve", "-revision", "1", "team", "plan"},
		{"approve", "-revision", "1", "-sha256", "bad", "team", "plan"},
	} {
		if _, err := parseMemberCommand(args); err == nil {
			t.Errorf("accepted invalid member arguments %q", args)
		}
	}
	pub, err := parseMemberCommand([]string{"publish", "-expected", "0", "-file", "notes", "team", "plan"})
	if err != nil || pub.number != 0 || pub.service != "https://team.example" || pub.tokenFile != "reader.token" {
		t.Fatalf("expected new document with member environment: %+v, %v", pub, err)
	}
	approve, err := parseMemberCommand([]string{"approve", "-url", "https://review.example", "-token-file", "reviewer.token",
		"-revision", "12", "-sha256", digest, "team", "plan"})
	if err != nil || approve.number != 12 || approve.digest != digest || approve.service != "https://review.example" || approve.tokenFile != "reviewer.token" {
		t.Fatalf("expected command line overrides and reviewed revision: %+v, %v", approve, err)
	}
}

func TestMemberFileSizeBoundary(t *testing.T) {
	path := filepath.Join(t.TempDir(), "boundary")
	const limit = 8
	content := bytes.Repeat([]byte{'x'}, limit)
	for _, data := range [][]byte{nil, content, append(bytes.Clone(content), 'y')} {
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
		got, err := readMemberFile(path, limit)
		if len(data) == limit {
			if err != nil || !bytes.Equal(got, data) {
				t.Fatal("failed to read a file exactly at the limit")
			}
		} else if err == nil {
			t.Errorf("accepted %d bytes", len(data))
		}
	}
	if _, err := readMemberFile(filepath.Dir(path), limit); err == nil {
		t.Fatal("accepted a directory")
	}
}
