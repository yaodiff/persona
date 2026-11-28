package backup

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/nwverse/persona/server/internal/managerconf"
	"github.com/nwverse/persona/server/internal/recordingwire"
	"github.com/nwverse/persona/server/internal/secretkey"
)

var optionalRootFiles = []string{
	"secret.key.prev", "tailcat-hub.json", "handles.key", "apns.json", "apns_key.p8",
	"tailcat.json", "link.json", "daemons.json", "docker/db-password",
}

var optionalRuntimeDirectories = []string{"extensions", "skills", "hub.machines_kept", "tls"}

// userContentDirectories hold the owner's files (installed extensions and
// skills), copied in full like the knowledge base: a name such as yarn.lock or
// meeting.log there is content, not a runtime artifact.
var userContentDirectories = map[string]bool{"extensions": true, "skills": true}

// collectPayload copies the runtime files and the knowledge base into staging.
// notes are warnings for the operator (a knowledge base that is not there).
func collectPayload(home, staging string, manifest *Manifest) (notes []string, err error) {
	home, err = filepath.Abs(home)
	if err != nil {
		return nil, err
	}
	locator, kbDir, err := knowledgeBaseLocator(home)
	if err != nil {
		return nil, err
	}
	if err := refuseKnowledgeBaseOverlap(home, kbDir); err != nil {
		return nil, err
	}
	managerDir := filepath.Join(home, "managers", "assistant")
	if own := managerSecretFile(home); !samePath(own, managerconf.OwnFile(home, managerconf.SecretKey)) {
		return nil, errors.New("the built-in manager still reads the hub's secret.key; finish its key-file migration before backup")
	}
	// A knowledge base inside a runtime directory (kb_dir "kb" puts it in
	// managers/assistant) is collected once, as the knowledge base, not also
	// as part of that directory.
	if err := copyTreeExcept(managerDir, filepath.Join(staging, "runtime", "managers", "assistant"), true, kbDir); err != nil {
		return nil, fmt.Errorf("collect the built-in manager files: %w", err)
	}
	manifest.RuntimeDirectories = append(manifest.RuntimeDirectories, "managers/assistant")
	if err := copyRequired(filepath.Join(home, "hub.json"), filepath.Join(staging, "runtime", "hub.json")); err != nil {
		return nil, err
	}
	if err := copyRequired(filepath.Join(home, "secret.key"), filepath.Join(staging, "runtime", "secret.key")); err != nil {
		return nil, err
	}
	for _, name := range optionalRootFiles {
		if err := copyOptional(filepath.Join(home, filepath.FromSlash(name)), filepath.Join(staging, "runtime", filepath.FromSlash(name))); err != nil {
			return nil, err
		}
	}
	nextKeys, err := filepath.Glob(filepath.Join(home, "secret.key.next-*"))
	if err != nil {
		return nil, err
	}
	for _, name := range nextKeys {
		if err := copyOptional(name, filepath.Join(staging, "runtime", filepath.Base(name))); err != nil {
			return nil, err
		}
	}
	for _, name := range optionalRuntimeDirectories {
		source := filepath.Join(home, name)
		info, err := os.Lstat(source)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if !info.IsDir() {
			return nil, fmt.Errorf("runtime path %s is not a directory", name)
		}
		if err := copyTreeExcept(source, filepath.Join(staging, "runtime", name), !userContentDirectories[name], kbDir); err != nil {
			return nil, fmt.Errorf("collect %s: %w", name, err)
		}
		manifest.RuntimeDirectories = append(manifest.RuntimeDirectories, name)
	}
	manifest.KnowledgeBase = locator
	if info, err := os.Lstat(kbDir); err != nil {
		// Not a failure: an install may have no knowledge base yet. But a path
		// that is only missing for now (an unmounted drive, a mistyped kb_dir)
		// would be recorded as no knowledge base, so the operator is told.
		manifest.KnowledgeBase.Present = false
		reason := "does not exist"
		if !errors.Is(err, os.ErrNotExist) {
			reason = "cannot be read (" + err.Error() + ")"
		}
		notes = append(notes, fmt.Sprintf("warning: the knowledge base %s %s, so this bundle records no knowledge base and restoring it will leave whatever knowledge base the install has in place. "+
			"If it should be there (an unmounted drive, a mistyped kb_dir), fix that and create the backup again", kbDir, reason))
	} else if !info.IsDir() {
		return nil, errors.New("the knowledge-base path is not a directory")
	} else {
		manifest.KnowledgeBase.Present = true
		// Ephemeral hub-transcribed meeting audio (recordings/<id>/attachments/audio/)
		// is deleted after final minutes are written and must not outlive that
		// deletion in a backup bundle.
		if err := copyTreeFiltered(kbDir, filepath.Join(staging, "kb"), false, "", recordingwire.IsMeetingAudioPath); err != nil {
			return nil, fmt.Errorf("collect the knowledge base: %w", err)
		}
	}
	for _, required := range []string{"runtime/managers/assistant/manager.json", managerKeyPath} {
		name := filepath.Join(staging, filepath.FromSlash(required))
		if info, err := os.Lstat(name); err != nil || !info.Mode().IsRegular() {
			return nil, fmt.Errorf("required runtime file %s is missing or not regular", required)
		}
		if err := os.Chmod(name, 0o600); err != nil {
			return nil, err
		}
	}
	hubKey, err := secretkey.ReadKey(filepath.Join(home, "secret.key"))
	if err != nil {
		return nil, fmt.Errorf("read the hub encryption key: %w", err)
	}
	managerKey, err := secretkey.ReadKey(managerSecretFile(home))
	if err != nil {
		return nil, fmt.Errorf("read the built-in manager encryption key: %w", err)
	}
	manifest.HubKeyFingerprint = secretkey.Fingerprint(hubKey)
	manifest.ManagerKeyFingerprint = secretkey.Fingerprint(managerKey)
	if manifest.Manager.SealingKey != "" && manifest.Manager.SealingKey != manifest.ManagerKeyFingerprint {
		return nil, errors.New("the built-in manager database names a different secret.key than the one on disk, usually because its key rotation is unfinished (a secret.key.next-* file is pending). " +
			"If so, start the built-in manager once so it finishes the rotation (it resumes at startup), then stop it and retry; the rotation cannot be rolled back after its commit")
	}
	return notes, nil
}

// runtimeTargets are the paths under home that restore replaces, other than
// the knowledge base; runtimeDirectoryTargets are the directories among them.
func runtimeTargets(home string) []string {
	targets := []string{filepath.Join(home, "hub.json"), filepath.Join(home, "secret.key")}
	for _, name := range optionalRootFiles {
		targets = append(targets, filepath.Join(home, filepath.FromSlash(name)))
	}
	return append(targets, runtimeDirectoryTargets(home)...)
}

func runtimeDirectoryTargets(home string) []string {
	targets := []string{filepath.Join(home, "managers", "assistant")}
	for _, name := range optionalRuntimeDirectories {
		targets = append(targets, filepath.Join(home, name))
	}
	return targets
}

// pathWithin reports whether child is parent or lies under it.
func pathWithin(parent, child string) bool {
	rel, err := filepath.Rel(parent, child)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// pathForms are a path as given and with the symbolic links of its longest
// existing prefix resolved.
func pathForms(name string) []string {
	return []string{filepath.Clean(name), resolveExisting(name)}
}

// refuseKnowledgeBaseOverlap refuses a knowledge base that restore could not
// replace without moving something else with it: one that holds the runtime
// directory (home, its parent or any ancestor, /), which would move the whole
// install, the previous-files directory with it, into the knowledge base's
// previous copy; one that is or holds a runtime file or directory restore
// replaces as well (hub.json, managers, managers/assistant, extensions...),
// which would push the item just restored into the previous files; one inside
// a runtime file; and one inside a previous-files directory. A knowledge base
// inside a runtime directory (kb_dir "kb" is managers/assistant/kb) is fine:
// that directory is restored first, without it, and the knowledge base is
// then installed inside it. Paths are compared as given and with their
// symbolic links resolved.
func refuseKnowledgeBaseOverlap(home, kb string) error {
	directories := map[string]bool{}
	for _, name := range runtimeDirectoryTargets(home) {
		directories[name] = true
	}
	for _, kbForm := range pathForms(kb) {
		for _, homeForm := range pathForms(home) {
			if pathWithin(kbForm, homeForm) {
				return fmt.Errorf("refusing the knowledge base %s: it holds the runtime directory %s, so restoring it would move the whole install into its previous copy. "+
					"Nothing was changed; restore into a runtime directory outside it, or set kb_dir to a directory of its own", kb, home)
			}
			if rel, err := filepath.Rel(homeForm, kbForm); err == nil && strings.HasPrefix(strings.Split(filepath.ToSlash(rel), "/")[0], ".persona-backup-previous-") {
				return fmt.Errorf("refusing the knowledge base %s: it lies inside a previous-files directory of an earlier restore. Nothing was changed", kb)
			}
		}
		for _, target := range runtimeTargets(home) {
			for _, targetForm := range pathForms(target) {
				if pathWithin(kbForm, targetForm) || (!directories[target] && pathWithin(targetForm, kbForm)) {
					return fmt.Errorf("refusing the knowledge base %s: it is or holds %s, which the backup holds and restore replaces as well. "+
						"Nothing was changed; set kb_dir to a directory of its own (the default is data; a directory inside managers/assistant, extensions, skills or hub.machines_kept is fine too)", kb, target)
				}
			}
		}
	}
	return nil
}

func managerSecretFile(home string) string {
	return filepath.Join(home, "managers", "assistant", "secret.key")
}

func samePath(a, b string) bool {
	aa, errA := filepath.Abs(a)
	bb, errB := filepath.Abs(b)
	return errA == nil && errB == nil && filepath.Clean(aa) == filepath.Clean(bb)
}

func copyRequired(source, destination string) error {
	if err := copyFile(source, destination); err != nil {
		return fmt.Errorf("collect required file %s: %w", filepath.Base(source), err)
	}
	return os.Chmod(destination, 0o600)
}

func copyOptional(source, destination string) error {
	if _, err := os.Lstat(source); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	return copyFile(source, destination)
}

func ephemeralRuntimeFile(name string) bool {
	base := filepath.Base(name)
	return base == ".lock" || strings.HasSuffix(base, ".lock") || strings.HasSuffix(base, ".pid") ||
		strings.HasSuffix(base, ".sock") || strings.HasSuffix(base, ".log") || strings.Contains(base, ".tmp-")
}

// copyTree copies the regular files under source. Sockets are skipped
// everywhere (live endpoints, not data); skipRuntime also skips lock, pid, log
// and temporary files, and is only for directories the product itself writes,
// never for the owner's files.
func copyTree(source, destination string, skipRuntime bool) error {
	return copyTreeExcept(source, destination, skipRuntime, "")
}

// copyTreeExcept is copyTree leaving out the directory except (a knowledge
// base inside source), compared as given and with symbolic links resolved.
func copyTreeExcept(source, destination string, skipRuntime bool, except string) error {
	return copyTreeFiltered(source, destination, skipRuntime, except, nil)
}

// copyTreeFiltered is copyTreeExcept also skipping any path under source whose
// slash-separated relative path satisfies skipRel (a directory skips its whole
// subtree).
func copyTreeFiltered(source, destination string, skipRuntime bool, except string, skipRel func(string) bool) error {
	info, err := os.Lstat(source)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%s is not a real directory", source)
	}
	if err := os.MkdirAll(destination, 0o700); err != nil {
		return err
	}
	var excluded []string
	if except != "" {
		excluded = pathForms(except)
	}
	resolvedSource := resolveExisting(source)
	return filepath.WalkDir(source, func(name string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if name == source {
			return nil
		}
		rel, err := filepath.Rel(source, name)
		if err != nil {
			return err
		}
		if skipRel != nil && skipRel(filepath.ToSlash(rel)) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		for _, form := range excluded {
			if form == filepath.Clean(name) || form == filepath.Join(resolvedSource, rel) {
				if entry.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
		}
		if entry.IsDir() {
			if entry.Type()&os.ModeSymlink != 0 {
				return fmt.Errorf("symbolic-link directory %s is not supported", name)
			}
			return os.MkdirAll(filepath.Join(destination, rel), 0o700)
		}
		if entry.Type()&os.ModeSocket != 0 || (skipRuntime && ephemeralRuntimeFile(name)) {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("non-regular runtime file %s is not supported", name)
		}
		return copyFile(name, filepath.Join(destination, rel))
	})
}

func copyFile(source, destination string) error {
	info, err := os.Lstat(source)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file", source)
	}
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		return err
	}
	out, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if info.Mode().Perm()&0o111 != 0 {
		if err := out.Chmod(0o700); err != nil {
			out.Close()
			return err
		}
	}
	_, copyErr := io.Copy(out, in)
	if syncErr := out.Sync(); copyErr == nil {
		copyErr = syncErr
	}
	if closeErr := out.Close(); copyErr == nil {
		copyErr = closeErr
	}
	return copyErr
}

// renameFile and removeAll are os.Rename and os.RemoveAll; tests replace them
// to make the file transaction's steps fail.
var (
	renameFile = os.Rename
	removeAll  = os.RemoveAll
)

type restoreItem struct {
	source  string
	target  string
	runtime bool
}

type restoreRecord struct {
	item          restoreItem
	backup        string
	temporary     string
	previousMoved bool
	installed     bool
}

type fileTransaction struct {
	id           string
	backupRoot   string
	externalRoot string
	records      []*restoreRecord
}

// applyFiles installs what the bundle has, each copy staged beside its target
// before the target is renamed aside; what the bundle has no copy of stays as it
// is. The replaced files remain in mode-0700 transaction directories after
// success.
func applyFiles(ctx context.Context, home, staging string, manifest *Manifest, allowExternal bool, id string) (previous, previousExternal string, rollback func() error, returnErr error) {
	home, err := filepath.Abs(home)
	if err != nil {
		return "", "", nil, err
	}
	items, err := restoreItems(home, staging, manifest, allowExternal)
	if err != nil {
		return "", "", nil, err
	}
	transaction := &fileTransaction{id: id, backupRoot: filepath.Join(home, ".persona-backup-previous-"+id)}
	if err := os.MkdirAll(transaction.backupRoot, 0o700); err != nil {
		return "", "", nil, err
	}
	committed := false
	defer func() {
		if !committed {
			returnErr = errors.Join(returnErr, transaction.rollback())
		}
	}()
	for _, item := range items {
		if err := ctx.Err(); err != nil {
			return "", "", nil, err
		}
		if err := transaction.apply(item); err != nil {
			return "", "", nil, err
		}
	}
	committed = true
	if emptyDirectory(transaction.backupRoot) {
		_ = os.Remove(transaction.backupRoot)
		transaction.backupRoot = ""
	}
	if transaction.externalRoot != "" && emptyDirectory(transaction.externalRoot) {
		_ = os.Remove(transaction.externalRoot)
		transaction.externalRoot = ""
	}
	return transaction.backupRoot, transaction.externalRoot, transaction.rollback, nil
}

// leftInPlaceNotes names what the manifest records as absent or empty (no
// knowledge base, a runtime directory that held no files) while this install
// has it. Restore does not reproduce that absence: it leaves the install's copy
// where it is, untouched, and says so. Call it before the files change.
func leftInPlaceNotes(home, staging string, manifest *Manifest, allowExternal bool) ([]string, error) {
	home, err := filepath.Abs(home)
	if err != nil {
		return nil, err
	}
	var notes []string
	for _, name := range manifest.RuntimeDirectories {
		staged := filepath.Join(staging, "runtime", filepath.FromSlash(name))
		if _, err := os.Lstat(staged); err == nil && !emptyDirectory(staged) {
			continue
		}
		if _, err := os.Lstat(filepath.Join(home, filepath.FromSlash(name))); err == nil {
			notes = append(notes, fmt.Sprintf("note: the bundle records an empty %s; this install's %s was left in place, untouched", name, name))
		}
	}
	if manifest.KnowledgeBase.Present {
		return notes, nil
	}
	kbTarget, err := knowledgeBaseTarget(home, manifest, allowExternal)
	if err != nil {
		return nil, err
	}
	if _, err := os.Lstat(kbTarget); err != nil {
		return notes, nil
	}
	// A knowledge base inside a runtime directory the bundle replaces goes with
	// that directory into the previous files.
	for _, directory := range runtimeDirectoryTargets(home) {
		rel, _ := filepath.Rel(home, directory)
		staged := filepath.Join(staging, "runtime", rel)
		if _, err := os.Lstat(staged); err == nil && pathWithin(directory, kbTarget) {
			return append(notes, fmt.Sprintf("note: the bundle records no knowledge base; this install's knowledge base %s lies inside %s, which restore replaced, so it is kept with that directory in the previous files", kbTarget, filepath.ToSlash(rel))), nil
		}
	}
	return append(notes, fmt.Sprintf("note: the bundle records no knowledge base; this install's knowledge base %s was left in place, untouched", kbTarget)), nil
}

// dockerPasswordKept reports whether restore leaves the install's own
// docker/db-password in place although the bundle carries one.
func dockerPasswordKept(home string, manifest *Manifest) bool {
	for _, entry := range manifest.Files {
		if entry.Path == "runtime/docker/db-password" {
			_, err := os.Lstat(filepath.Join(home, "docker", "db-password"))
			return err == nil
		}
	}
	return false
}

// knowledgeBaseTarget is where the bundle's knowledge base belongs.
func knowledgeBaseTarget(home string, manifest *Manifest, allowExternal bool) (string, error) {
	if manifest.KnowledgeBase.External != "" {
		if !allowExternal {
			return "", errors.New("this bundle has an external knowledge base; rerun with --allow-external-kb to replace that directory")
		}
		return manifest.KnowledgeBase.External, nil
	}
	rel := manifest.KnowledgeBase.Relative
	if rel == "" {
		rel = "data"
	}
	kbTarget := filepath.Join(home, filepath.FromSlash(rel))
	// Only an approved external knowledge base may lie outside the runtime directory.
	if inside, err := filepath.Rel(home, kbTarget); err != nil || inside == "." || inside == ".." || strings.HasPrefix(inside, ".."+string(filepath.Separator)) {
		return "", errors.New("the knowledge-base relative path leaves the runtime directory")
	}
	return kbTarget, nil
}

func restoreItems(home, staging string, manifest *Manifest, allowExternal bool) ([]restoreItem, error) {
	var items []restoreItem
	for _, name := range []string{"hub.json", "secret.key"} {
		items = append(items, restoreItem{source: filepath.Join(staging, "runtime", name), target: filepath.Join(home, name), runtime: true})
	}
	for _, name := range optionalRootFiles {
		// A Docker install's database password stays: the role's password is
		// not part of the bundle, so the new or surviving cluster only accepts
		// the one its own file names, and a bundle's copy (from the cluster it
		// was taken on) would stop the containers from starting. Only an install
		// with none gets the bundle's.
		if name == "docker/db-password" {
			if _, err := os.Lstat(filepath.Join(home, filepath.FromSlash(name))); err == nil {
				continue
			}
		}
		items = append(items, restoreItem{source: filepath.Join(staging, "runtime", filepath.FromSlash(name)), target: filepath.Join(home, filepath.FromSlash(name)), runtime: true})
	}
	// A pending rotation key (secret.key.next-<fingerprint>) may be the only copy
	// of a key: one the install has is never moved, and the bundle's is installed
	// only under a name the install does not have (the name is the key's
	// fingerprint, so a file of the same name holds the same key).
	for _, entry := range manifest.Files {
		rel, ok := strings.CutPrefix(entry.Path, "runtime/")
		if !ok || strings.Contains(rel, "/") || !strings.HasPrefix(rel, "secret.key.next-") {
			continue
		}
		target := filepath.Join(home, rel)
		if _, err := os.Lstat(target); err == nil {
			continue
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		items = append(items, restoreItem{source: filepath.Join(staging, "runtime", rel), target: target, runtime: true})
	}
	// The runtime directories come before the knowledge base, which may lie
	// inside one of them.
	for _, name := range append([]string{"managers/assistant"}, optionalRuntimeDirectories...) {
		items = append(items, restoreItem{source: filepath.Join(staging, "runtime", filepath.FromSlash(name)), target: filepath.Join(home, filepath.FromSlash(name)), runtime: true})
	}
	kbTarget, err := knowledgeBaseTarget(home, manifest, allowExternal)
	if err != nil {
		return nil, err
	}
	if err := refuseKnowledgeBaseOverlap(home, kbTarget); err != nil {
		return nil, err
	}
	kbSource := filepath.Join(staging, "kb")
	if manifest.KnowledgeBase.Present {
		if err := os.MkdirAll(kbSource, 0o700); err != nil {
			return nil, err
		}
	}
	items = append(items, restoreItem{source: kbSource, target: kbTarget, runtime: manifest.KnowledgeBase.External == ""})
	return items, nil
}

func (t *fileTransaction) apply(item restoreItem) error {
	// What the bundle has no copy of is not the bundle's to replace: it stays
	// in the live install, untouched (never moved aside with nothing in its
	// place).
	if _, err := os.Lstat(item.source); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	record := &restoreRecord{item: item}
	t.records = append(t.records, record)
	parent := filepath.Dir(item.target)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return err
	}
	record.temporary = filepath.Join(parent, fmt.Sprintf(".%s.persona-new-%s-%d", filepath.Base(item.target), t.id, len(t.records)))
	if err := copyRecursive(item.source, record.temporary); err != nil {
		return err
	}
	backupRoot := t.backupRoot
	if !item.runtime {
		if t.externalRoot == "" {
			t.externalRoot = filepath.Join(filepath.Dir(item.target), fmt.Sprintf(".%s.persona-backup-previous-%s", filepath.Base(item.target), t.id))
			if err := os.MkdirAll(t.externalRoot, 0o700); err != nil {
				return err
			}
		}
		backupRoot = t.externalRoot
	}
	record.backup = filepath.Join(backupRoot, fmt.Sprintf("%03d-%s", len(t.records), filepath.Base(item.target)))
	if _, err := os.Lstat(item.target); err == nil {
		if err := os.MkdirAll(filepath.Dir(record.backup), 0o700); err != nil {
			return err
		}
		if err := renameFile(item.target, record.backup); err != nil {
			return fmt.Errorf("preserve %s before restore: %w", filepath.Base(item.target), err)
		}
		record.previousMoved = true
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := renameFile(record.temporary, item.target); err != nil {
		return fmt.Errorf("install staged %s: %w", filepath.Base(item.target), err)
	}
	record.installed = true
	return secretkey.SyncDir(filepath.Dir(item.target))
}

// rollback puts every original back. The previous-files directories are
// removed only once empty: an original that could not be put back stays in
// them, and the error names it, its directory, and what to do.
func (t *fileTransaction) rollback() error {
	var errs []error
	for i := len(t.records) - 1; i >= 0; i-- {
		record := t.records[i]
		if record.installed {
			if err := removeAll(record.item.target); err != nil {
				if record.previousMoved {
					errs = append(errs, fmt.Errorf("remove the restored %s so that its original can go back: %w", record.item.target, err))
				} else {
					errs = append(errs, fmt.Errorf("remove %s, which the restore installed and this install did not have: %w; remove it by hand", record.item.target, err))
				}
			} else {
				record.installed = false
			}
		}
		if record.previousMoved {
			if err := renameFile(record.backup, record.item.target); err != nil {
				errs = append(errs, fmt.Errorf("put the original %s back from %s: %w", record.item.target, record.backup, err))
			} else {
				record.previousMoved = false
			}
		}
		if record.temporary != "" {
			_ = os.RemoveAll(record.temporary)
		}
	}
	var kept []string
	for _, root := range []string{t.backupRoot, t.externalRoot} {
		if root == "" {
			continue
		}
		if err := os.Remove(root); err != nil && !errors.Is(err, os.ErrNotExist) {
			kept = append(kept, root)
		}
	}
	if len(kept) > 0 {
		errs = append(errs, fmt.Errorf("the file rollback is incomplete; the originals it could not put back are preserved in %s, each as NNN-<name> (NNN is the order restore replaced them in). "+
			"Move each back to its place by hand (removing what the restore installed there first), then delete the emptied directory", strings.Join(kept, " and ")))
	}
	return errors.Join(errs...)
}

func copyRecursive(source, destination string) error {
	info, err := os.Lstat(source)
	if err != nil {
		return err
	}
	if info.Mode().IsRegular() {
		return copyFile(source, destination)
	}
	if !info.IsDir() {
		return errors.New("staged restore input is not a regular file or directory")
	}
	return copyTree(source, destination, false)
}

func emptyDirectory(name string) bool {
	entries, err := os.ReadDir(name)
	return err == nil && len(entries) == 0
}
