package node

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// git.review: every file that changed in a scope with its hunks, for the web's Review panel (docs/web/review.md,
// #151). The working tree is compared with HEAD (uncommitted), where the branch left the default branch (branch), or
// with a commit (commit); turn compares two working-tree checkpoints, or the latest checkpoint with the working tree.
// New files Git does not know yet count as added. Read only: no index, no lock (gitCmd), nothing written. The files
// file.read refuses are listed without their content, as git.show leaves them out.

const (
	maxReviewFiles = 1000
	// A new file bigger than this is not read to count its lines.
	reviewCountBytes = 32 << 20
)

// What one answer carries of the hunks, and of one file's; a file over its share comes without its hunks (Big) and
// is asked for on its own, which carries more. Variables for the tests.
var (
	reviewPage  = reviewBudget{left: 4 << 20, fileBytes: 256 << 10, fileLines: 3000}
	reviewAlone = reviewBudget{left: 2 << 20, fileBytes: 2 << 20, fileLines: 20000}
)

var hunkHead = lazyre.New(`^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@ ?(.*)$`)

func reviewErr(code, msg string) error { return &rpc.Error{Code: code, Message: msg} }

func (n *Node) gitReview(ctx context.Context, p proto.GitReviewRequest) (*proto.GitReview, error) {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	switch p.Scope {
	case proto.ReviewUncommitted, proto.ReviewBranch, proto.ReviewCommit, proto.ReviewTurn:
	default:
		return nil, reviewErr(proto.CodeBadScope, "scope is uncommitted, branch, commit or turn")
	}
	dir, err := n.gitDir(p.Cwd)
	if err != nil {
		return nil, err
	}
	// A folder that is gone (a removed worktree) is said as such, not as a folder outside any repository.
	if _, err := os.Stat(dir); errors.Is(err, os.ErrNotExist) {
		return nil, reviewErr(proto.CodeNoFolder, "the folder is not there")
	}
	root, err := git(ctx, dir, "rev-parse", "--show-toplevel")
	if err != nil || strings.TrimSpace(root) == "" {
		return nil, reviewErr(proto.CodeNotGit, "not a Git repository")
	}
	root = strings.TrimSpace(root)
	res := &proto.GitReview{Root: root, Scope: p.Scope, Files: []proto.GitReviewFile{}}
	if c, ok := commitInfo(ctx, root, "HEAD"); ok {
		res.Head = &proto.GitReviewHead{Hash: c.hash, Short: c.short}
		if b, err := git(ctx, root, "symbolic-ref", "-q", "--short", "HEAD"); err == nil {
			res.Head.Branch = strings.TrimSpace(b)
		}
	}
	base, target, err := reviewBaseTarget(ctx, root, p, res)
	if err != nil {
		return nil, err
	}

	spec, err := reviewSpec(p.Paths)
	if err != nil {
		return nil, err
	}
	alone := len(p.Paths) > 0
	files, err := n.reviewFiles(ctx, root, base, target, p.Scope == proto.ReviewTurn && p.Live, spec)
	if err != nil {
		return nil, err
	}
	if len(files) > maxReviewFiles {
		files, res.Truncated = files[:maxReviewFiles], true
	}
	markGenerated(ctx, root, files)

	page := reviewPage
	if alone {
		page = reviewAlone
	}
	if err := n.reviewHunks(ctx, root, base, target, spec, files, &page); err != nil {
		return nil, err
	}
	for i := range files {
		if target == "" && files[i].Untracked && !files[i].Refused {
			n.untrackedHunks(root, &files[i], &page)
		}
	}
	res.Files = files
	return res, nil
}

type commit struct{ hash, short, subject string }

func commitInfo(ctx context.Context, root, rev string) (commit, bool) {
	o, err := git(ctx, root, "log", "-n", "1", "--format=%H%x1f%h%x1f%s", rev, "--")
	f := strings.SplitN(strings.TrimRight(o, "\n"), "\x1f", 3)
	if err != nil || len(f) != 3 || f[0] == "" {
		return commit{}, false
	}
	return commit{f[0], f[1], f[2]}, true
}

func reviewBaseTarget(ctx context.Context, root string, p proto.GitReviewRequest, res *proto.GitReview) (string, string, error) {
	if p.Scope != proto.ReviewTurn {
		base, err := reviewBase(ctx, root, p, res)
		return base, "", err
	}
	return reviewTurn(ctx, root, p, res)
}

// reviewTurn chooses the checkpoints a turn review compares. Without an explicit Target, it shows the latest completed
// turn: the newest checkpoint against the one before it (or HEAD for the first one). Live asks for the newest
// checkpoint against the working tree, for the turn now running.
func reviewTurn(ctx context.Context, root string, p proto.GitReviewRequest, res *proto.GitReview) (string, string, error) {
	if strings.TrimSpace(p.Session) == "" {
		return "", "", reviewErr(proto.CodeBadScope, "turn needs a session")
	}
	if p.Live && p.Target != "" {
		return "", "", reviewErr(proto.CodeBadScope, "turn takes a target checkpoint or live, not both")
	}
	refs, err := checkpointRefs(ctx, root, p.Session)
	if err != nil {
		return "", "", err
	}
	res.Session = p.Session
	byHash := make(map[string]checkpointRef, len(refs))
	for _, r := range refs {
		byHash[r.Hash] = r
	}
	setBase := func(hash string) (string, error) {
		if hash == "" {
			o, err := git(ctx, root, "hash-object", "-t", "tree", "--stdin")
			res.Base = proto.GitReviewBase{Empty: true}
			return strings.TrimSpace(o), err
		}
		c, ok := commitInfo(ctx, root, hash)
		if !ok {
			return "", reviewErr(proto.CodeBadBase, "not a commit of this repository: "+hash)
		}
		res.Base.Hash, res.Base.Short, res.Base.Subject = c.hash, c.short, c.subject
		return c.hash, nil
	}
	setTarget := func(ref checkpointRef) (string, error) {
		c, ok := commitInfo(ctx, root, ref.Hash)
		if !ok {
			return "", reviewErr(proto.CodeBadBase, "not a checkpoint of this session: "+ref.Hash)
		}
		res.Target = &proto.GitReviewTarget{Hash: c.hash, Short: c.short, Subject: c.subject}
		res.Turn = ref.Turn
		return c.hash, nil
	}
	headHash := ""
	if res.Head != nil {
		headHash = res.Head.Hash
	}
	baseAllowed := func(hash string) bool {
		return hash == "" || strings.HasPrefix(headHash, hash) || byHash[hash].Hash != ""
	}
	resolve := func(hash string) (checkpointRef, bool) {
		if r, ok := byHash[hash]; ok {
			return r, true
		}
		var found checkpointRef
		matches := 0
		for _, r := range refs {
			if strings.HasPrefix(r.Hash, hash) {
				found, matches = r, matches+1
			}
		}
		return found, matches == 1
	}

	if p.Base != "" || p.Target != "" {
		if strings.HasPrefix(p.Base, "-") || strings.ContainsAny(p.Base, " \t\n:") {
			return "", "", reviewErr(proto.CodeBadBase, "not a checkpoint of this session: "+p.Base)
		}
		var targetRef checkpointRef
		targetParent := ""
		var baseRef checkpointRef
		baseIsCheckpoint := false
		if p.Target != "" {
			var ok bool
			targetRef, ok = resolve(p.Target)
			if !ok {
				return "", "", reviewErr(proto.CodeBadBase, "not a checkpoint of this session: "+p.Target)
			}
			targetParent, _ = checkpointParent(ctx, root, targetRef.Hash)
		}
		if p.Base != "" {
			baseRef, baseIsCheckpoint = resolve(p.Base)
		}
		if p.Base != "" && !p.Live && !baseAllowed(p.Base) && !baseIsCheckpoint &&
			(targetParent == "" || !strings.HasPrefix(targetParent, p.Base)) {
			return "", "", reviewErr(proto.CodeBadBase, "not a checkpoint of this session: "+p.Base)
		}
		baseHash := p.Base
		if baseIsCheckpoint {
			baseHash = baseRef.Hash
		} else if p.Base != "" && targetParent != "" && strings.HasPrefix(targetParent, p.Base) {
			baseHash = targetParent
		}
		if baseHash == "" && p.Target != "" {
			if previous, ok := checkpointBefore(refs, targetRef.Turn); ok {
				baseHash = previous.Hash
			} else if targetParent != "" {
				baseHash = targetParent
			}
		}
		if p.Base != "" && p.Target == "" && !p.Live && baseIsCheckpoint {
			next, ok := checkpointAfter(refs, baseRef.Turn)
			if !ok {
				return "", "", reviewErr(proto.CodeNoCheckpoint, "no completed checkpoint after "+p.Base+" in this session")
			}
			targetRef = next
		}
		base, err := setBase(baseHash)
		if err != nil || (p.Target == "" && !p.Live && targetRef.Turn == 0) {
			if err != nil {
				return "", "", err
			}
			return "", "", reviewErr(proto.CodeBadScope, "turn needs a target checkpoint or live")
		}
		if p.Live {
			res.Target = &proto.GitReviewTarget{Working: true}
			if baseIsCheckpoint {
				res.Turn = baseRef.Turn
			}
			return base, "", nil
		}
		target, err := setTarget(targetRef)
		return base, target, err
	}

	if len(refs) == 0 {
		return "", "", reviewErr(proto.CodeNoCheckpoint, "the session has no completed turn checkpoint yet")
	}
	latest := refs[len(refs)-1]
	if p.Live {
		b, err := setBase(latest.Hash)
		if err != nil {
			return "", "", err
		}
		res.Target = &proto.GitReviewTarget{Working: true}
		res.Turn = latest.Turn
		return b, "", nil
	}
	base := ""
	if len(refs) > 1 {
		base = refs[len(refs)-2].Hash
	} else if parent, ok := checkpointParent(ctx, root, latest.Hash); ok {
		base = parent
	}
	b, err := setBase(base)
	if err != nil {
		return "", "", err
	}
	t, err := setTarget(latest)
	return b, t, err
}

func checkpointParent(ctx context.Context, root, hash string) (string, bool) {
	out, err := git(ctx, root, "rev-parse", "--verify", "-q", hash+"^")
	if err != nil || strings.TrimSpace(out) == "" {
		return "", false
	}
	return strings.TrimSpace(out), true
}

func checkpointBefore(refs []checkpointRef, turn int64) (checkpointRef, bool) {
	for i := len(refs) - 1; i >= 0; i-- {
		if refs[i].Turn < turn {
			return refs[i], true
		}
	}
	return checkpointRef{}, false
}

func checkpointAfter(refs []checkpointRef, turn int64) (checkpointRef, bool) {
	for _, r := range refs {
		if r.Turn > turn {
			return r, true
		}
	}
	return checkpointRef{}, false
}

// reviewBase is the tree the working tree is compared with, written into res.Base. Before the first commit it is the
// empty tree: everything is new.
func reviewBase(ctx context.Context, root string, p proto.GitReviewRequest, res *proto.GitReview) (string, error) {
	empty := func() (string, error) {
		o, err := git(ctx, root, "hash-object", "-t", "tree", "--stdin") // the empty tree, in the repository's hash
		res.Base = proto.GitReviewBase{Empty: true, Ref: res.Base.Ref}
		return strings.TrimSpace(o), err
	}
	use := func(rev string) (string, error) {
		c, ok := commitInfo(ctx, root, rev)
		if !ok {
			return "", reviewErr(proto.CodeBadBase, "not a commit of this repository: "+rev)
		}
		res.Base.Hash, res.Base.Short, res.Base.Subject = c.hash, c.short, c.subject
		return c.hash, nil
	}
	base := strings.TrimSpace(p.Base)
	if strings.HasPrefix(base, "-") || strings.ContainsAny(base, " \t\n:") {
		return "", reviewErr(proto.CodeBadBase, "not a commit of this repository: "+base)
	}
	switch p.Scope {
	case proto.ReviewCommit:
		if base == "" {
			return "", reviewErr(proto.CodeBadScope, "commit needs a base")
		}
		return use(base)
	case proto.ReviewBranch:
		ref := base
		if ref == "" {
			if ref = defaultBranch(ctx, root); ref == "" {
				return "", reviewErr(proto.CodeNoDefaultBranch, "no default branch (origin/HEAD, main or master)")
			}
		} else if _, ok := commitInfo(ctx, root, ref); !ok {
			return "", reviewErr(proto.CodeBadBase, "not a branch of this repository: "+ref)
		}
		res.Base.Ref = ref
		if res.Head == nil {
			return empty()
		}
		mb, err := git(ctx, root, "merge-base", "HEAD", ref)
		if err != nil || strings.TrimSpace(mb) == "" {
			return "", reviewErr(proto.CodeNoMergeBase, "HEAD and "+ref+" have no commit in common")
		}
		return use(strings.TrimSpace(mb))
	}
	if res.Head == nil {
		return empty()
	}
	return use(res.Head.Hash)
}

// defaultBranch is the branch a branch was made from: what origin/HEAD points at, else origin/main, origin/master,
// main, master, whichever is there first; "" when none is.
func defaultBranch(ctx context.Context, root string) string {
	if o, err := git(ctx, root, "symbolic-ref", "-q", "--short", "refs/remotes/origin/HEAD"); err == nil && strings.TrimSpace(o) != "" {
		return strings.TrimSpace(o)
	}
	for _, ref := range []string{"origin/main", "origin/master", "main", "master"} {
		full := "refs/heads/" + ref
		if strings.HasPrefix(ref, "origin/") {
			full = "refs/remotes/" + ref
		}
		if _, err := git(ctx, root, "rev-parse", "--verify", "-q", full); err == nil {
			return ref
		}
	}
	return ""
}

// reviewSpec is the pathspec for the paths asked for, each taken literally from the root; none for every file.
func reviewSpec(paths []string) ([]string, error) {
	var spec []string
	for _, p := range paths {
		c := filepath.ToSlash(filepath.Clean(strings.TrimSpace(p)))
		if c == "." || c == "" || c == ".." || strings.HasPrefix(c, "../") || strings.HasPrefix(c, "/") {
			return nil, errors.New("bad path: " + p)
		}
		spec = append(spec, ":(top,literal)"+c)
	}
	return spec, nil
}

// reviewFiles lists the files that changed (git's raw diff and its line counts) and, when target is empty, the new
// files in the working tree, in path order, with the ones file.read refuses marked Refused.
func (n *Node) reviewFiles(ctx context.Context, root, base, target string, liveTurn bool, spec []string) ([]proto.GitReviewFile, error) {
	revs := []string{base}
	if target != "" {
		revs = append(revs, target)
	}
	rawArgs := append([]string{"diff", "-M", "--raw", "-z", "--no-ext-diff"}, revs...)
	rawArgs = append(rawArgs, "--")
	rawArgs = append(rawArgs, spec...)
	raw, err := git(ctx, root, rawArgs...)
	if err != nil {
		return nil, err
	}
	files := parseRawDiff(raw)
	counts := map[string][2]int{}
	numstat := append([]string{"diff", "-M", "--numstat", "-z", "--no-ext-diff", "--no-textconv"}, revs...)
	numstat = append(numstat, "--")
	numstat = append(numstat, spec...)
	if o, err := git(ctx, root, numstat...); err == nil {
		counts = parseNumstat(o)
	}
	seen := map[string]bool{}
	for i := range files {
		f := &files[i]
		seen[f.Path] = true
		if c, ok := counts[f.Path]; ok {
			if c[0] < 0 {
				f.Binary = true
			} else {
				f.Added, f.Deleted = c[0], c[1]
			}
		}
	}
	if liveTurn {
		var err error
		files, err = n.keepLiveUntracked(ctx, root, base, files, seen)
		if err != nil {
			return nil, err
		}
	}
	if target == "" {
		o, err := git(ctx, root, append([]string{"ls-files", "--others", "--exclude-standard", "-z", "--"}, spec...)...)
		if err != nil {
			return nil, err
		}
		for _, p := range strings.Split(o, "\x00") {
			// a folder: another repository inside this one, whose files are its own
			if p == "" || strings.HasSuffix(p, "/") || seen[p] {
				continue
			}
			files = append(files, proto.GitReviewFile{Path: p, Status: "added", Untracked: true})
		}
	}
	refused := map[string]bool{}
	isRefused := func(p string) bool {
		r, ok := refused[p]
		if !ok {
			r = n.guardRepoPath(root, p) != nil
			refused[p] = r
		}
		return r
	}
	for i := range files {
		f := &files[i]
		if isRefused(f.Path) || f.OldPath != "" && isRefused(f.OldPath) {
			f.Refused, f.Added, f.Deleted = true, 0, 0
		}
	}
	sort.SliceStable(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files, nil
}

// keepLiveUntracked fixes the one asymmetry of a live checkpoint diff: a file the checkpoint captured while it was
// untracked in the index is reported deleted when the next turn's working tree still has it. If the disk still has the
// same blob, it is no change; otherwise it is a new untracked file. The path stays in seen so the later untracked scan
// does not add it twice.
func (n *Node) keepLiveUntracked(ctx context.Context, root, base string, files []proto.GitReviewFile, seen map[string]bool) ([]proto.GitReviewFile, error) {
	candidates := make([]int, 0)
	for i := range files {
		if files[i].Status == "deleted" && !files[i].Untracked && !files[i].Refused {
			candidates = append(candidates, i)
		}
	}
	if len(candidates) == 0 {
		return files, nil
	}
	trackedOut, err := git(ctx, root, "ls-files", "-z", "--cached")
	if err != nil {
		return nil, err
	}
	tracked := map[string]bool{}
	for _, p := range splitNul(trackedOut) {
		tracked[p] = true
	}
	out := files[:0]
	for i := range files {
		f := files[i]
		keep := true
		if f.Status == "deleted" && !tracked[f.Path] {
			if st, err := os.Lstat(filepath.Join(root, filepath.FromSlash(f.Path))); err == nil && !st.IsDir() {
				same, err := sameWorkingBlob(ctx, root, base, f.Path, st)
				if err != nil {
					return nil, err
				}
				if same {
					seen[f.Path] = true
					keep = false
				} else {
					f.Status, f.Untracked, f.Added, f.Deleted, f.Hunks, f.Big = "added", true, 0, 0, nil, false
					seen[f.Path] = true
				}
			}
		}
		if keep {
			out = append(out, f)
		}
	}
	return out, nil
}

func sameWorkingBlob(ctx context.Context, root, base, path string, st os.FileInfo) (bool, error) {
	blob, ok := checkpointBlob(ctx, root, base, path)
	if !ok {
		return false, nil
	}
	working, err := workingBlob(ctx, root, path, st)
	if err != nil {
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		return false, nil
	}
	return blob == working, nil
}

func checkpointBlob(ctx context.Context, root, base, path string) (string, bool) {
	out, err := git(ctx, root, "ls-tree", "-z", base, "--", ":(top,literal)"+path)
	if err != nil {
		return "", false
	}
	rec := strings.TrimSuffix(out, "\x00")
	if i := strings.IndexByte(rec, '\t'); i >= 0 {
		rec = rec[:i]
	}
	f := strings.Fields(rec)
	if len(f) < 3 || f[1] != "blob" {
		return "", false
	}
	return f[2], true
}

func workingBlob(ctx context.Context, root, path string, st os.FileInfo) (string, error) {
	if st.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(filepath.Join(root, filepath.FromSlash(path)))
		if err != nil {
			return "", err
		}
		out, err := gitWithEnvInput(ctx, root, nil, strings.NewReader(target), "hash-object", "--stdin", "--path="+path)
		return strings.TrimSpace(out), err
	}
	if !st.Mode().IsRegular() {
		return "", errors.New("not a regular file")
	}
	f, err := os.Open(filepath.Join(root, filepath.FromSlash(path)))
	if err != nil {
		return "", err
	}
	defer f.Close()
	out, err := gitWithEnvInput(ctx, root, nil, f, "hash-object", "--stdin", "--path="+path)
	return strings.TrimSpace(out), err
}

// parseRawDiff reads `git diff --raw -z`: ":srcmode dstmode src dst STATUS\0path\0" ("\0from\0to\0" for a rename or
// copy), in the order the patch has them.
func parseRawDiff(out string) []proto.GitReviewFile {
	files := []proto.GitReviewFile{}
	recs := strings.Split(out, "\x00")
	for i := 0; i < len(recs); i++ {
		r := recs[i]
		if !strings.HasPrefix(r, ":") {
			continue
		}
		f := strings.Fields(r)
		if len(f) < 5 || i+1 >= len(recs) {
			break
		}
		kind := f[4][:1]
		g := proto.GitReviewFile{Path: recs[i+1], Status: reviewStatus(kind)}
		i++
		if (kind == "R" || kind == "C") && i+1 < len(recs) {
			g.OldPath, g.Path = g.Path, recs[i+1]
			i++
		}
		files = append(files, g)
	}
	return files
}

func reviewStatus(kind string) string {
	switch kind {
	case "A":
		return "added"
	case "D":
		return "deleted"
	case "R":
		return "renamed"
	case "C":
		return "copied"
	case "T":
		return "type_changed"
	}
	return "modified" // M, and U (a conflict: its markers are in the working tree)
}

// markGenerated marks the files .gitattributes calls linguist-generated (folded in the panel, as on GitHub).
func markGenerated(ctx context.Context, root string, files []proto.GitReviewFile) {
	if len(files) == 0 {
		return
	}
	var in strings.Builder
	for _, f := range files {
		in.WriteString(f.Path)
		in.WriteByte(0)
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cmd := gitCmd(ctx, root, "check-attr", "-z", "--stdin", "linguist-generated")
	cmd.Stdin = strings.NewReader(in.String())
	out, err := cmd.Output()
	if err != nil {
		return
	}
	gen := map[string]bool{}
	recs := strings.Split(string(out), "\x00")
	for i := 0; i+2 < len(recs); i += 3 {
		if v := recs[i+2]; v == "set" || v == "true" {
			gen[recs[i]] = true
		}
	}
	for i := range files {
		files[i].Generated = gen[files[i].Path]
	}
}

// reviewBudget is what is left of a page's hunks, and how much of them one file may take.
type reviewBudget struct {
	left                 int
	fileBytes, fileLines int
}

// reviewHunks reads the patch of the files Git knows (all but the refused ones) into their hunks. The patch has them
// in the raw diff's order, a "diff --git" section each; a file over its share, and every file once the page is full,
// comes without them (Big).
func (n *Node) reviewHunks(ctx context.Context, root, base, target string, spec []string, files []proto.GitReviewFile, page *reviewBudget) error {
	var order []*proto.GitReviewFile // the files the patch has, in its order
	var exclude []string
	for i := range files {
		f := &files[i]
		switch {
		case f.Refused:
			for _, p := range []string{f.Path, f.OldPath} {
				if p != "" && !f.Untracked {
					exclude = append(exclude, ":(top,exclude,literal)"+p)
				}
			}
		case !f.Untracked:
			order = append(order, f)
		}
	}
	if len(order) == 0 {
		return nil
	}
	// the patch lists them as the raw diff did (by the new path, renames paired)
	sort.SliceStable(order, func(i, j int) bool { return order[i].Path < order[j].Path })
	if len(spec) > 0 {
		exclude = append(spec, exclude...)
	}
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	revs := []string{base}
	if target != "" {
		revs = append(revs, target)
	}
	args := append([]string{"diff", "-M", "--no-color", "--no-ext-diff", "--no-textconv", "--src-prefix=a/", "--dst-prefix=b/", "-U3"}, revs...)
	args = append(args, "--")
	args = append(args, exclude...)
	cmd := gitCmd(ctx, root, args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	defer func() {
		cancel() // the rest of a patch over the page is not read
		_ = cmd.Wait()
	}()
	r := bufio.NewReaderSize(stdout, 64<<10)
	at := -1
	var cur *proto.GitReviewFile
	var bytes, lines int
	inHunk := false
	for {
		line, err := r.ReadString('\n')
		if line == "" && err != nil {
			break
		}
		line = strings.TrimSuffix(line, "\n")
		if strings.HasPrefix(line, "diff --git ") && cur != nil && cur.Status == "type_changed" && headerNames(line, cur.Path) {
			// a type change (file to symlink or back) comes as a deletion and then a creation of the same path: the
			// second section is the same file's, and its share keeps counting both
			inHunk = false
		} else if strings.HasPrefix(line, "diff --git ") {
			if cur != nil && cur.Big {
				cur.Hunks = nil
			}
			at++
			inHunk, bytes, lines = false, 0, 0
			if at >= len(order) || !headerNames(line, order[at].Path) {
				// not the file expected (it changed between the two commands): the rest are asked for on their own
				for _, f := range order[min(at, len(order)):] {
					f.Big, f.Hunks = !f.Binary, nil
				}
				return nil
			}
			cur = order[at]
		} else if cur != nil {
			if !page.line(cur, line, &inHunk, &bytes, &lines) {
				break // the page is full
			}
		}
		if err != nil {
			break
		}
	}
	if cur != nil && cur.Big {
		cur.Hunks = nil
	}
	// the files after the one the page ended in are asked for on their own
	for _, f := range order[max(at+1, 0):] {
		if !f.Binary && f.Added+f.Deleted > 0 {
			f.Big = true
		}
	}
	return nil
}

// line takes one line of a file's section of the patch: its header lines (the blob ids, binary), a hunk's head, or a
// line of the hunk while the file and the page have room. False when the page is full (the file is then Big).
func (page *reviewBudget) line(f *proto.GitReviewFile, line string, inHunk *bool, bytes, lines *int) bool {
	if !*inHunk {
		switch {
		case strings.HasPrefix(line, "index "):
			if fs := strings.Fields(line); len(fs) > 1 {
				if _, after, ok := strings.Cut(fs[1], ".."); ok && strings.Trim(after, "0") != "" {
					f.Revision = after
				}
			}
		case strings.HasPrefix(line, "Binary files "), strings.HasPrefix(line, "GIT binary patch"):
			f.Binary = true
		}
	}
	if m := hunkHead.FindStringSubmatch(line); m != nil {
		*inHunk = true
		if !f.Big {
			f.Hunks = append(f.Hunks, proto.GitHunk{Header: m[5], OldStart: hunkStart(m[1], m[2]), NewStart: hunkStart(m[3], m[4]), Lines: []string{}})
		}
		return true
	}
	if !*inHunk || f.Big {
		return true
	}
	switch line[:min(1, len(line))] {
	case " ", "+", "-", "\\", "":
	default:
		return true
	}
	*bytes += len(line) + 1
	*lines++
	if *bytes > page.fileBytes || *lines > page.fileLines {
		f.Big, f.Hunks = true, nil // its share is given back
		page.left += *bytes - len(line) - 1
		return true
	}
	if page.left < len(line)+1 {
		f.Big, f.Hunks = true, nil
		page.left += *bytes - len(line) - 1
		return false
	}
	page.left -= len(line) + 1
	h := &f.Hunks[len(f.Hunks)-1]
	h.Lines = append(h.Lines, line)
	return true
}

// hunkStart is where a side of a hunk starts: the line the header names, or the one after it when the side has no
// lines ("-a,0": after line a).
func hunkStart(start, count string) int {
	s, _ := strconv.Atoi(start)
	if count == "0" {
		return s + 1
	}
	return s
}

// headerNames says whether a "diff --git a/… b/…" line is the file at path (its new side), quoted or not.
func headerNames(line, path string) bool {
	return strings.HasSuffix(line, " b/"+path) || strings.HasSuffix(line, ` "b/`+gitQuote(path)+`"`)
}

// gitQuote is how Git writes a path with odd characters in a header (core.quotepath off: bytes over 0x7f as they are).
func gitQuote(p string) string {
	var b strings.Builder
	for i := 0; i < len(p); i++ {
		c := p[i]
		switch c {
		case '"', '\\':
			b.WriteByte('\\')
			b.WriteByte(c)
		case '\a':
			b.WriteString(`\a`)
		case '\b':
			b.WriteString(`\b`)
		case '\t':
			b.WriteString(`\t`)
		case '\n':
			b.WriteString(`\n`)
		case '\v':
			b.WriteString(`\v`)
		case '\f':
			b.WriteString(`\f`)
		case '\r':
			b.WriteString(`\r`)
		default:
			if c < 0x20 || c == 0x7f {
				b.WriteString(fmt.Sprintf("\\%03o", c))
				continue
			}
			b.WriteByte(c)
		}
	}
	return b.String()
}

// untrackedHunks is a new file Git does not know yet as one hunk of added lines (what `git diff --no-index` would
// show); a link's is where it points, as Git stores it.
func (n *Node) untrackedHunks(root string, f *proto.GitReviewFile, page *reviewBudget) {
	full := filepath.Join(root, filepath.FromSlash(f.Path))
	st, err := os.Lstat(full)
	if err != nil {
		return
	}
	var data []byte
	switch {
	case st.Mode()&os.ModeSymlink != 0:
		to, err := os.Readlink(full)
		if err != nil {
			return
		}
		data = []byte(to)
	case st.Mode().IsRegular():
		file, err := os.Open(full)
		if err != nil {
			return
		}
		defer file.Close()
		data, err = io.ReadAll(io.LimitReader(file, int64(page.fileBytes)+1))
		if err != nil {
			return
		}
		if isBinary(data) {
			f.Binary = true
			return
		}
		if len(data) > page.fileBytes {
			f.Big = true
			if st.Size() <= reviewCountBytes {
				if all, err := os.ReadFile(full); err == nil {
					f.Added = strings.Count(string(all), "\n")
					if len(all) > 0 && all[len(all)-1] != '\n' {
						f.Added++
					}
				}
			}
			return
		}
	default:
		return
	}
	if len(data) == 0 {
		return
	}
	text := string(data)
	ending := strings.HasSuffix(text, "\n")
	lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	f.Added = len(lines)
	if len(lines) > page.fileLines || page.left < len(data) {
		f.Big = true
		return
	}
	page.left -= len(data)
	h := proto.GitHunk{OldStart: 1, NewStart: 1, Lines: make([]string, 0, len(lines)+1)}
	for _, l := range lines {
		h.Lines = append(h.Lines, "+"+l)
	}
	if !ending {
		h.Lines = append(h.Lines, `\ No newline at end of file`)
	}
	f.Hunks = []proto.GitHunk{h}
}

// isBinary: a NUL in the first 8000 bytes, as Git decides.
func isBinary(data []byte) bool {
	return strings.IndexByte(string(data[:min(len(data), 8000)]), 0) >= 0
}
