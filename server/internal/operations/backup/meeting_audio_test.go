package backup

import (
	"archive/tar"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func listArchiveMembers(t *testing.T, bundle string) []string {
	t.Helper()
	file, err := os.Open(bundle)
	if err != nil {
		fatal(t, err)
	}
	defer file.Close()
	var names []string
	reader := tar.NewReader(file)
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return names
		}
		if err != nil {
			fatal(t, err)
		}
		names = append(names, header.Name)
	}
}

// Ephemeral hub-transcribed meeting audio under recordings/<id>/attachments/audio/
// must never enter a whole-install backup bundle (regardless of kb_dir layout),
// while the recording's metadata, transcripts, minutes, photos, and non-recording
// attachments remain backed up and restored.
func TestCreateExcludesHubMeetingAudioAcrossKnowledgeBaseLayouts(t *testing.T) {
	const recID = "0f8fad5b-d9cb-469f-a165-70867728950e"
	for _, layout := range []struct {
		name            string
		configureKB     func(t *testing.T, fixture *backupFixture) string
		allowExternalKB bool
	}{
		{
			name: "default",
			configureKB: func(_ *testing.T, fixture *backupFixture) string {
				return filepath.Join(fixture.home, "data")
			},
		},
		{
			name: "nested",
			configureKB: func(t *testing.T, fixture *backupFixture) string {
				config := `{"database_name":"persona_manager","kb_dir":"kb"}`
				writeFixtureFile(t, filepath.Join(fixture.home, "managers", "assistant", "manager.json"), []byte(config))
				return filepath.Join(fixture.home, "managers", "assistant", "kb")
			},
		},
		{
			name: "external",
			configureKB: func(t *testing.T, fixture *backupFixture) string {
				external := filepath.Join(t.TempDir(), "external-kb")
				raw, err := json.Marshal(map[string]string{"database_name": "persona_manager", "kb_dir": external})
				if err != nil {
					fatal(t, err)
				}
				writeFixtureFile(t, filepath.Join(fixture.home, "managers", "assistant", "manager.json"), raw)
				return external
			},
			allowExternalKB: true,
		},
	} {
		t.Run(layout.name, func(t *testing.T) {
			fixture := newBackupFixture(t, "persona_manager")
			kb := layout.configureKB(t, fixture)

			excluded := []string{
				filepath.Join("recordings", recID, "attachments", "audio", "0.pcm"),
				filepath.Join("recordings", recID, "attachments", "audio", "index.json"),
				filepath.Join("recordings", recID, "attachments", "audio", "nested", "16000.pcm"),
				filepath.Join("recordings", "sample-meeting", "attachments", "audio", "marker.bin"),
			}
			for _, rel := range excluded {
				writeFixtureFile(t, filepath.Join(kb, rel), []byte("ephemeral-audio-"+rel))
			}
			if err := os.MkdirAll(filepath.Join(kb, "recordings", "empty-audio", "attachments", "audio"), 0o700); err != nil {
				fatal(t, err)
			}

			kept := map[string][]byte{
				filepath.Join("recordings", recID, "meta.json"):                  []byte(`{"id":"` + recID + `"}`),
				filepath.Join("recordings", recID, "hub_segments.json"):          []byte(`{"rev":1}`),
				filepath.Join("recordings", recID, "transcript.md"):              []byte("00:00 hello\n"),
				filepath.Join("recordings", recID, "minutes.md"):                 []byte("# Minutes\n"),
				filepath.Join("recordings", recID, "attachments", "p1.jpg"):      []byte("meeting-photo"),
				filepath.Join("recordings", recID, "attachments", "audio.jpg"):   []byte("photo-named-audio"),
				filepath.Join("recordings", ".deleted.json"):                     []byte(`{}`),
				filepath.Join("chats", "c1", "attachments", "audio", "note.ogg"): []byte("chat-audio-attachment"),
			}
			for rel, content := range kept {
				writeFixtureFile(t, filepath.Join(kb, rel), content)
			}

			operator := fixture.operator()
			if _, err := operator.Create(context.Background(), fixture.options()); err != nil {
				fatal(t, err)
			}

			manifest, _, err := readBundleManifest(fixture.output)
			if err != nil {
				fatal(t, err)
			}
			if !manifest.KnowledgeBase.Present {
				fatal(t, "manifest recorded no knowledge base")
			}

			members := listArchiveMembers(t, fixture.output)
			for _, name := range members {
				if strings.HasPrefix(name, "kb/recordings/") && strings.Contains(name, "/attachments/audio/") {
					errorf(t, "bundle archive contains hub meeting audio member %s", name)
				}
				if strings.HasPrefix(name, "kb/recordings/") && strings.HasSuffix(name, "/attachments/audio") {
					errorf(t, "bundle archive contains hub meeting audio path %s", name)
				}
			}
			for _, entry := range manifest.Files {
				if strings.HasPrefix(entry.Path, "kb/recordings/") &&
					(strings.Contains(entry.Path, "/attachments/audio/") || strings.HasSuffix(entry.Path, "/attachments/audio")) {
					errorf(t, "manifest lists hub meeting audio %s", entry.Path)
				}
			}
			for rel := range kept {
				member := "kb/" + filepath.ToSlash(rel)
				if !hasMember(manifest, member) {
					errorf(t, "manifest omitted kept knowledge-base member %s", member)
				}
			}

			report, err := inspectBundle(context.Background(), fixture.output)
			if err != nil {
				fatal(t, err)
			}
			if report.KnowledgeBase.State != "included" {
				errorf(t, "inspection knowledge_base.state = %q, want included", report.KnowledgeBase.State)
			}

			fixture.mutate(t)
			if _, err := operator.Restore(context.Background(), Options{
				Home:            fixture.home,
				Bundle:          fixture.output,
				WritesQuiesced:  true,
				AllowExternalKB: layout.allowExternalKB,
			}); err != nil {
				fatal(t, err)
			}
			for rel, content := range kept {
				assertFile(t, filepath.Join(kb, rel), content)
			}
			for _, rel := range excluded {
				if _, err := os.Lstat(filepath.Join(kb, rel)); !errors.Is(err, os.ErrNotExist) {
					errorf(t, "restored knowledge base still contains excluded audio %s: %v", rel, err)
				}
			}
			if _, err := os.Lstat(filepath.Join(kb, "recordings", recID, "attachments", "audio")); !errors.Is(err, os.ErrNotExist) {
				errorf(t, "restored knowledge base still contains audio directory: %v", err)
			}
		})
	}
}

// A knowledge base that contains only ephemeral hub meeting audio is still
// recorded as present (empty), passes offline inspection, and restores without
// the audio.
func TestCreateWithOnlyMeetingAudioProducesEmptyKnowledgeBase(t *testing.T) {
	fixture := newBackupFixture(t, "persona_manager")
	kb := filepath.Join(fixture.home, "data")
	if err := os.RemoveAll(kb); err != nil {
		fatal(t, err)
	}
	writeFixtureFile(t, filepath.Join(kb, "recordings", "0f8fad5b-d9cb-469f-a165-70867728950e", "attachments", "audio", "0.pcm"), []byte("pcm-bytes"))

	operator := fixture.operator()
	if _, err := operator.Create(context.Background(), fixture.options()); err != nil {
		fatal(t, err)
	}
	manifest, _, err := readBundleManifest(fixture.output)
	if err != nil {
		fatal(t, err)
	}
	if !manifest.KnowledgeBase.Present {
		fatal(t, "manifest should record the knowledge base as present")
	}
	for _, entry := range manifest.Files {
		if strings.HasPrefix(entry.Path, "kb/") {
			errorf(t, "unexpected knowledge-base member %s", entry.Path)
		}
	}
	report, err := inspectBundle(context.Background(), fixture.output)
	if err != nil {
		fatal(t, err)
	}
	if report.KnowledgeBase.State != "empty" || report.KnowledgeBase.Files != 0 {
		errorf(t, "inspection knowledge_base = %+v, want empty with 0 files", report.KnowledgeBase)
	}
	fixture.mutate(t)
	if _, err := operator.Restore(context.Background(), Options{Home: fixture.home, Bundle: fixture.output, WritesQuiesced: true}); err != nil {
		fatal(t, err)
	}
	if _, err := os.Lstat(filepath.Join(kb, "recordings", "0f8fad5b-d9cb-469f-a165-70867728950e", "attachments", "audio")); !errors.Is(err, os.ErrNotExist) {
		errorf(t, "restored knowledge base contains meeting audio directory: %v", err)
	}
}

// A symbolic link placed at recordings/<id>/attachments/audio is skipped rather
// than causing backup collection to fail on a non-regular file.
func TestCreateSkipsSymlinkedMeetingAudioDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		skip(t, "symlinks require elevated privileges on Windows")
	}
	fixture := newBackupFixture(t, "persona_manager")
	kb := filepath.Join(fixture.home, "data")
	target := filepath.Join(t.TempDir(), "audio-target")
	writeFixtureFile(t, filepath.Join(target, "0.pcm"), []byte("pcm"))
	linkParent := filepath.Join(kb, "recordings", "linked-audio", "attachments")
	if err := os.MkdirAll(linkParent, 0o700); err != nil {
		fatal(t, err)
	}
	if err := os.Symlink(target, filepath.Join(linkParent, "audio")); err != nil {
		fatal(t, err)
	}
	if _, err := fixture.operator().Create(context.Background(), fixture.options()); err != nil {
		fatal(t, err)
	}
	for _, name := range listArchiveMembers(t, fixture.output) {
		if strings.Contains(name, "linked-audio") {
			errorf(t, "bundle archive contains skipped audio symlink member %s", name)
		}
	}
}
