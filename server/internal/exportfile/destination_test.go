package exportfile_test

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/nwverse/persona/server/internal/exportfile"
)

func TestPublicationFailureOrRaceLeavesNoPartialFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "conversation.md")
	dest, err := exportfile.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer dest.Close()
	if err := dest.Publish(context.Background(), func(out io.Writer) error {
		_, _ = out.Write([]byte("unpublished"))
		return errors.New("write failed")
	}); err == nil {
		t.Fatal("write failure ignored")
	}
	if files, _ := os.ReadDir(dir); len(files) != 0 {
		t.Fatal("failed file left a partial file")
	}
	if err := os.WriteFile(path, []byte("existing"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := dest.Publish(context.Background(), func(out io.Writer) error {
		_, err := out.Write([]byte("new"))
		return err
	}); !errors.Is(err, exportfile.ErrExists) {
		t.Fatalf("write race result: %v", err)
	}
	data, err := os.ReadFile(path)
	files, _ := os.ReadDir(dir)
	if err != nil || string(data) != "existing" || len(files) != 1 {
		t.Fatalf("file race changed existing file: %q %v %v", data, files, err)
	}
}

func TestCancellationBeforePublicationCleansUp(t *testing.T) {
	dir := t.TempDir()
	dest, err := exportfile.Open(filepath.Join(dir, "captions.vtt"))
	if err != nil {
		t.Fatal(err)
	}
	defer dest.Close()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	err = dest.Publish(ctx, func(out io.Writer) error {
		_, err := io.WriteString(out, "complete content")
		cancel()
		return err
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("publication returned %v", err)
	}
	files, err := os.ReadDir(dir)
	if err != nil || len(files) != 0 {
		t.Fatalf("files after cancellation: %v, %v", files, err)
	}
}
