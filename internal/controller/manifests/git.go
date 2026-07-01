package manifests

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Repo wraps a directory with the small subset of git operations the
// controller needs: ensure-initialized, add+commit, push, pull, status.
// All git work shells out via the system `git` binary — no go-git, no
// libgit2. That keeps the dependency tree small and means the operator's
// existing git config (commit signing, ssh keys, credentials) just works.
//
// Repo is safe to call from multiple goroutines: a per-Repo mutex serializes
// git invocations so concurrent applies don't collide on the index lock.
type Repo struct {
	dir          string
	branch       string
	remote       string
	pushMode     string
	pushInterval time.Duration

	// lock serializes git invocations. A few apply ops landing in quick
	// succession could otherwise race the index.lock.
	lock chanLock
}

// chanLock is a buffered-channel mutex that integrates with context for
// cancellable Lock. Standard sync.Mutex doesn't honor ctx.
type chanLock chan struct{}

func newChanLock() chanLock { return make(chanLock, 1) }

func (l chanLock) acquire(ctx context.Context) error {
	select {
	case l <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (l chanLock) release() { <-l }

// RepoOptions configures a Repo. Dir is required; all others have safe
// defaults.
type RepoOptions struct {
	Dir          string
	Branch       string // default "main"
	Remote       string // empty disables push
	PushMode     string // "onCommit" (default if Remote set), "manual", "periodic"
	PushInterval time.Duration
}

// NewRepo returns a Repo for the given directory. Does not create or
// initialize the directory — call EnsureInit for that.
func NewRepo(opts RepoOptions) (*Repo, error) {
	if opts.Dir == "" {
		return nil, errors.New("git repo dir required")
	}
	r := &Repo{
		dir:          opts.Dir,
		branch:       opts.Branch,
		remote:       opts.Remote,
		pushMode:     opts.PushMode,
		pushInterval: opts.PushInterval,
		lock:         newChanLock(),
	}
	if r.branch == "" {
		r.branch = "main"
	}
	if r.pushMode == "" {
		if r.remote != "" {
			r.pushMode = PushModeOnCommit
		} else {
			r.pushMode = PushModeManual
		}
	}
	return r, nil
}

// Push mode constants. Free-form strings in config; constants here for
// type safety in callers.
const (
	PushModeOnCommit = "onCommit"
	PushModePeriodic = "periodic"
	PushModeManual   = "manual"
)

// Dir returns the working tree directory.
func (r *Repo) Dir() string { return r.dir }

// Branch returns the configured branch name.
func (r *Repo) Branch() string { return r.branch }

// Remote returns the configured remote URL ("" if no remote).
func (r *Repo) Remote() string { return r.remote }

// PushMode returns the configured push mode.
func (r *Repo) PushMode() string { return r.pushMode }

// EnsureInit ensures r.dir is a git repository on r.branch. Idempotent: if
// a .git directory already exists, it's reused as-is (we don't try to
// rewrite branch or remote — that's the operator's job).
//
// On first init we set user.name + user.email to a "controller robot"
// identity so commits are valid even when the operator hasn't configured
// global git identity in the controller's environment (LaunchAgents
// don't inherit shell-set identity).
func (r *Repo) EnsureInit(ctx context.Context) error {
	if err := r.lock.acquire(ctx); err != nil {
		return err
	}
	defer r.lock.release()

	if _, err := os.Stat(filepath.Join(r.dir, ".git")); err == nil {
		return nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("stat .git: %w", err)
	}

	if err := os.MkdirAll(r.dir, 0o750); err != nil {
		return fmt.Errorf("create dir: %w", err)
	}
	if _, err := r.run(ctx, "init", "-b", r.branch); err != nil {
		return err
	}
	if _, err := r.run(ctx, "config", "user.name", "openctl-controller"); err != nil {
		return err
	}
	if _, err := r.run(ctx, "config", "user.email", "openctl-controller@localhost"); err != nil {
		return err
	}
	if r.remote != "" {
		if _, err := r.run(ctx, "remote", "add", "origin", r.remote); err != nil {
			return err
		}
	}
	return nil
}

// CommitAll stages all changes in r.dir and creates a commit with the
// given message. Returns ErrNothingToCommit if the working tree was clean
// (no apply/delete actually changed any file — common when the user re-
// applies an unchanged manifest).
//
// Does NOT push. Push() is a separate call so push policy is decoupled
// from commit cadence.
func (r *Repo) CommitAll(ctx context.Context, message string) error {
	if err := r.lock.acquire(ctx); err != nil {
		return err
	}
	defer r.lock.release()

	if _, err := r.run(ctx, "add", "-A"); err != nil {
		return err
	}
	// Bail out if nothing was actually staged. `git diff --cached --quiet`
	// exits 0 if clean, 1 if there are staged changes. exec.Command turns
	// non-zero into an error; we treat error-with-exit-1 as "dirty" and no
	// error as "clean".
	if _, err := r.run(ctx, "diff", "--cached", "--quiet"); err == nil {
		return ErrNothingToCommit
	}
	if _, err := r.run(ctx, "commit", "-m", message); err != nil {
		return err
	}
	return nil
}

// ErrNothingToCommit is returned by CommitAll when there were no staged
// changes. Callers (typically the DiskMirror hook) usually swallow this.
var ErrNothingToCommit = errors.New("git: nothing to commit")

// Push pushes the current branch to origin. No-op when no remote is
// configured. Errors are returned to the caller; the dispatcher logs and
// swallows them so apply ops never fail because of a flaky remote.
func (r *Repo) Push(ctx context.Context) error {
	if r.remote == "" {
		return nil
	}
	if err := r.lock.acquire(ctx); err != nil {
		return err
	}
	defer r.lock.release()
	if _, err := r.run(ctx, "push", "-u", "origin", r.branch); err != nil {
		return err
	}
	return nil
}

// Pull runs `git pull --ff-only`. Advisory only — does NOT trigger any
// reapply or reconciliation; the operator is expected to call this only
// when they want their local tree updated from remote (e.g. before
// inspecting committed history).
func (r *Repo) Pull(ctx context.Context) error {
	if r.remote == "" {
		return nil
	}
	if err := r.lock.acquire(ctx); err != nil {
		return err
	}
	defer r.lock.release()
	if _, err := r.run(ctx, "pull", "--ff-only"); err != nil {
		return err
	}
	return nil
}

// Status is the snapshot returned by RepoService.GetStatus.
type Status struct {
	// Branch is the current HEAD branch name. Empty for a brand-new repo
	// with no commits yet.
	Branch string
	// HeadSHA is the abbreviated commit hash at HEAD ("" if no commits).
	HeadSHA string
	// Remote is the configured remote URL ("" if none).
	Remote string
	// Clean is true when the working tree has no staged or unstaged
	// changes.
	Clean bool
	// DirtyPaths is the porcelain list of changed paths (empty when Clean).
	DirtyPaths []string
	// Ahead is the count of local commits not on the remote tracking
	// branch. -1 if not measured (no remote / no tracking branch).
	Ahead int
	// Behind is the count of remote commits not yet in local. -1 if not
	// measured.
	Behind int
}

// Status returns the current repo status. Best-effort: any field whose
// query fails is left at its zero value.
func (r *Repo) Status(ctx context.Context) (Status, error) {
	if err := r.lock.acquire(ctx); err != nil {
		return Status{}, err
	}
	defer r.lock.release()

	s := Status{Remote: r.remote, Ahead: -1, Behind: -1}

	if branch, err := r.run(ctx, "rev-parse", "--abbrev-ref", "HEAD"); err == nil {
		s.Branch = strings.TrimSpace(string(branch))
	}
	// Empty branch means no commits yet (HEAD unresolvable); "HEAD" means
	// detached. Both fall back to the configured branch so the UI always
	// has something to display.
	if s.Branch == "" || s.Branch == "HEAD" {
		s.Branch = r.branch
	}
	if sha, err := r.run(ctx, "rev-parse", "--short", "HEAD"); err == nil {
		s.HeadSHA = strings.TrimSpace(string(sha))
	}
	if out, err := r.run(ctx, "status", "--porcelain"); err == nil {
		raw := strings.TrimSpace(string(out))
		if raw == "" {
			s.Clean = true
		} else {
			for line := range strings.SplitSeq(raw, "\n") {
				if line = strings.TrimSpace(line); line != "" {
					s.DirtyPaths = append(s.DirtyPaths, line)
				}
			}
		}
	}
	if r.remote != "" {
		// rev-list @{u}..HEAD -> ahead; HEAD..@{u} -> behind. Both fail
		// silently if no tracking branch exists yet.
		if out, err := r.run(ctx, "rev-list", "--count", "@{u}..HEAD"); err == nil {
			if n, perr := strconv.Atoi(strings.TrimSpace(string(out))); perr == nil {
				s.Ahead = n
			}
		}
		if out, err := r.run(ctx, "rev-list", "--count", "HEAD..@{u}"); err == nil {
			if n, perr := strconv.Atoi(strings.TrimSpace(string(out))); perr == nil {
				s.Behind = n
			}
		}
	}
	return s, nil
}

// StartPeriodicPush spawns a background goroutine that pushes every
// r.pushInterval. No-op if push mode isn't "periodic" or the interval is
// zero or no remote is configured. Stops when ctx is canceled.
func (r *Repo) StartPeriodicPush(ctx context.Context, log func(format string, args ...any)) {
	if r.pushMode != PushModePeriodic || r.pushInterval <= 0 || r.remote == "" {
		return
	}
	go func() {
		t := time.NewTicker(r.pushInterval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if err := r.Push(ctx); err != nil {
					if log != nil {
						log("git push (periodic): %v", err)
					}
				}
			}
		}
	}()
}

// run executes a git subcommand in r.dir and returns its stdout. stderr
// is appended to the error message on failure so the operator sees git's
// own complaint rather than a generic "exit status 1". Caller holds r.lock.
func (r *Repo) run(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", args...) // #nosec G204 -- args are constants or operator-supplied via config
	cmd.Dir = r.dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("git %s: %w (stderr: %s)",
			strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
}
