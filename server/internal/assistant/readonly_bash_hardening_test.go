package assistant

import (
	"encoding/json"
	"testing"
)

func TestReadonlyTaskSafeBashHardeningBypasses(t *testing.T) {
	blocked := []struct {
		name string
		cwd  string
		cmd  string
	}{
		// Guarded credential dirs (.config/gh, .config/gcloud) accessed via relative, absolute,
		// parent-dir, backslash, or chained git -C paths.
		{"cat relative .config/gh", "/Users/alice", `cat .config/gh/config.yml`},
		{"cat relative .config/gcloud", "/Users/alice", `cat .config/gcloud/credentials.db`},
		{"ls relative .config/gh", "/Users/alice", `ls .config/gh`},
		{"rg relative .config/gcloud", "/Users/alice", `rg token .config/gcloud`},
		{"cat abs .config/gcloud inside cwd", "/Users/alice", `cat /Users/alice/.config/gcloud/access_tokens.db`},
		{"cat child gh from .config cwd", "/Users/alice/.config", `cat gh/config.yml`},
		{"cat child gcloud from .config cwd", "/Users/alice/.config", `cat gcloud/credentials.db`},
		{"git diff relative .config/gcloud", "/Users/alice", `git diff /dev/null .config/gcloud/credentials.db`},
		{"git chained -C into .config/gh", "/Users/alice", `git -C .config -C gh status`},
		{"git chained -C into .config/gcloud", "/Users/alice", `git -C .config -C gcloud status`},
		{"cat backslash .env traversal", "/code/app", `cat '..\.env'`},
		{"cat backslash .config/gh", "/Users/alice", `cat '.config\gh\config.yml'`},

		// UNC paths, backslash root paths, Windows drive-relative paths, and git -C backslash roots.
		{"cat UNC share", "/code/app", `cat '\\server\share\secret.txt'`},
		{"cat slash UNC share", "/code/app", `cat //server/share/secret.txt`},
		{"cat backslash root Windows hosts", "/code/app", `cat '\Windows\System32\drivers\etc\hosts'`},
		{"cat Windows drive-relative secret", "/code/app", `cat C:secret.txt`},
		{"cat Windows drive-relative .config/gh", "/Users/alice", `cat 'C:.config\gh\config.yml'`},
		{"cat Windows drive-relative .ssh", "/Users/alice", `cat 'C:.ssh\config'`},
		{"git -C backslash root", "/code/app", `git -C '\Windows' status`},
		{"git -C UNC share", "/code/app", `git -C '\\server\share' status`},
		{"git -C drive-relative", "/code/app", `git -C C:repo status`},
		{"git diff UNC share", "/code/app", `git diff '\\server\share\secret.txt'`},
		{"git diff drive-relative .config/gh", "/Users/alice", `git diff 'C:.config\gh\config.yml'`},
		{"git diff drive-relative secret", "/code/app", `git diff C:secret.txt`},

		// git rev:path and :(magic) pathspec prefixes targeting sensitive paths.
		{"git show HEAD:.config/gh/config.yml", "/Users/alice", `git show HEAD:.config/gh/config.yml`},
		{"git show :0:.env.local", "/code/app", `git show :0:.env.local`},
		{"git diff :(top).config/gh/config.yml", "/Users/alice", `git diff -- ':(top).config/gh/config.yml'`},
		{"git diff :(icase,top)gh/config.yml from .config", "/Users/alice/.config", `git diff -- ':(icase,top)gh/config.yml'`},
		{"git diff :/.env", "/code/app", `git diff -- :/.env`},
		{"git show HEAD:../etc/passwd", "/code/app", `git show HEAD:../etc/passwd`},

		// git arbitrary file-read flags and bundled short-flag bypasses (including boolean -D).
		{"git diff -O orderfile attached", "/code/app", `git diff -O/etc/passwd`},
		{"git diff -O orderfile separate", "/code/app", `git diff -O /etc/passwd`},
		{"git diff -DO boolean D before O", "/code/app", `git diff -DO/etc/passwd`},
		{"git show -O orderfile attached", "/code/app", `git show -O/etc/passwd HEAD`},
		{"git show -DO boolean D before O", "/code/app", `git show -DO/etc/passwd HEAD`},
		{"git log -O orderfile attached", "/code/app", `git log -O/etc/passwd -n 1`},
		{"git log -DpO boolean D and p before O", "/code/app", `git log -n1 -DpO/etc/passwd`},
		{"git diff bundled -pO orderfile", "/code/app", `git diff -pO/etc/passwd`},
		{"git show bundled -po output", "/code/app", `git show -po/tmp/patch.diff HEAD`},
		{"git log bundled -px exec", "/code/app", `git log -pxsh -n 1`},
		{"git ls-files -X exclude-from attached", "/code/app", `git ls-files -X/etc/passwd`},
		{"git ls-files -X exclude-from separate", "/code/app", `git ls-files -X /etc/passwd`},
		{"git ls-files bundled -cX", "/code/app", `git ls-files -cX/etc/passwd`},
		{"git ls-files --exclude-from", "/code/app", `git ls-files --exclude-from=/etc/passwd`},
		{"git ls-files --exclude-from abbrev", "/code/app", `git ls-files --exclude-f=/etc/passwd`},
		{"git ls-files --exclude-per-directory", "/code/app", `git ls-files --exclude-per-directory=.env`},
		{"git rev-parse --resolve-git-dir", "/code/app", `git rev-parse --resolve-git-dir=/etc/passwd`},

		// gh boolean short flags misclassified as value-taking flags skip --web / -w / --watch.
		{"gh run view -e skips --web", "/code/app", `gh run view 123 -e --web`},
		{"gh run view -e skips -w", "/code/app", `gh run view 123 -e -w`},
		{"gh run view -v skips --web", "/code/app", `gh run view 123 -v --web`},
		{"gh pr view -b skips --web", "/code/app", `gh pr view 123 -b --web`},
		{"gh pr view -c skips --web", "/code/app", `gh pr view 123 -c --web`},
		{"gh pr view -j skips --web", "/code/app", `gh pr view 123 -j --web`},
		{"gh pr list -d skips --web", "/code/app", `gh pr list -d --web`},
		{"gh pr checks -e skips --watch", "/code/app", `gh pr checks 123 -e --watch`},
		{"gh issue view -u skips --web", "/code/app", `gh issue view 456 -u --web`},
		{"gh issue view -c skips --web", "/code/app", `gh issue view 456 -c --web`},
		{"gh run list -a skips --web", "/code/app", `gh run list -a --web`},
		{"gh pr view dangling -R", "/code/app", `gh pr view 123 -R`},
		{"gh pr diff dangling -e", "/code/app", `gh pr diff 123 -e`},

		// ls symlink dereference flags.
		{"ls -L dereference", "/code/app", `ls -L .`},
		{"ls -H dereference command line", "/code/app", `ls -H .`},
		{"ls bundled -lL", "/code/app", `ls -lL .`},
		{"ls --dereference", "/code/app", `ls --dereference .`},
		{"ls --dereference short abbrev --de", "/code/app", `ls --de .`},
		{"ls --dereference abbrev --der", "/code/app", `ls --der .`},
		{"ls --dereference-command-line", "/code/app", `ls --dereference-command-line .`},
	}

	for _, tc := range blocked {
		t.Run("block/"+tc.name, func(t *testing.T) {
			raw, _ := json.Marshal(map[string]string{"command": tc.cmd})
			if readonlyTaskSafeBash(raw, tc.cwd) {
				t.Fatalf("expected unsafe readonly bash to be blocked (cwd=%q): %q", tc.cwd, tc.cmd)
			}
		})
	}

	allowed := []struct {
		name string
		cwd  string
		cmd  string
	}{
		{"git diff -S pattern containing O", "/code/app", `git diff -SORDER HEAD~1`},
		{"git log -n20 --oneline", "/code/app", `git log -n20 --oneline`},
		{"git show HEAD:README.md", "/code/app", `git show HEAD:README.md`},
		{"git diff :/README.md", "/code/app", `git diff -- :/README.md`},
		{"git -C chained safe subdirs", "/code/app", `git -C server -C internal status -sb`},
		{"gh run view -e --log", "/code/app", `gh run view 123 -e --log`},
		{"gh run list -e push -L 5", "/code/app", `gh run list -e push -L 5`},
		{"gh pr diff -e attached", "/code/app", `gh pr diff 123 -e'*.go'`},
		{"gh repo view -b main", "/code/app", `gh repo view -b main`},
		{"ls -lh human readable", "/code/app", `ls -lh server/internal/assistant`},
	}
	for _, tc := range allowed {
		t.Run("allow/"+tc.name, func(t *testing.T) {
			raw, _ := json.Marshal(map[string]string{"command": tc.cmd})
			if !readonlyTaskSafeBash(raw, tc.cwd) {
				t.Fatalf("expected safe readonly bash to be allowed (cwd=%q): %q", tc.cwd, tc.cmd)
			}
		})
	}
}
