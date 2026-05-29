package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"
)

var snapRe = regexp.MustCompile(`^[a-f0-9]{8,64}$`)

func validSnapID(s string) bool { return s == "latest" || snapRe.MatchString(s) }

// safeRestoreTarget canonicalizes and confines target within root.
func safeRestoreTarget(root, target string) (string, error) {
	if strings.Contains(target, "..") {
		return "", fmt.Errorf("path contains ..")
	}
	abs, err := filepath.Abs(filepath.Clean(target))
	if err != nil {
		return "", err
	}
	rootAbs, _ := filepath.Abs(filepath.Clean(root))
	// 1. Lexical confinement: target must equal root or live under it.
	if !within(rootAbs, abs) {
		return "", fmt.Errorf("outside root")
	}
	// 2. Symlink safety: the deepest EXISTING ancestor of the target, after
	//    resolving symlinks, must stay on the same lineage as the resolved root
	//    (within it, equal to it, or an ancestor of it). A symlink in the path
	//    that diverges elsewhere (e.g. -> /etc) is rejected.
	realRoot := rootAbs
	if r, e := filepath.EvalSymlinks(rootAbs); e == nil {
		realRoot = r
	}
	anc := abs
	for {
		if _, e := os.Stat(anc); e == nil {
			break
		}
		parent := filepath.Dir(anc)
		if parent == anc {
			break
		}
		anc = parent
	}
	if realAnc, e := filepath.EvalSymlinks(anc); e == nil {
		if realAnc != realRoot && !within(realRoot, realAnc) && !within(realAnc, realRoot) {
			return "", fmt.Errorf("escapes root via symlink")
		}
	}
	return abs, nil
}

// within reports whether child is parent or is contained under parent.
func within(parent, child string) bool {
	if parent == child {
		return true
	}
	if !strings.HasSuffix(parent, string(os.PathSeparator)) {
		parent += string(os.PathSeparator)
	}
	return strings.HasPrefix(child, parent)
}

// safeResticPath rejects shell/space metacharacters in an ls path argument.
var pathSafeRe = regexp.MustCompile(`^[A-Za-z0-9_./-]*$`)

func safeResticPath(p string) bool { return p == "" || pathSafeRe.MatchString(p) }

type Runner struct {
	mu      sync.Mutex
	running bool
	store   *Store
	scripts string // /opt/backup/scripts
	logDir  string
}

func NewRunner(store *Store, scripts, logDir string) *Runner {
	return &Runner{store: store, scripts: scripts, logDir: logDir}
}

func (r *Runner) Busy() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.running
}

// RunBackup executes the backup script with low priority; serialized via TryLock.
// Returns immediately; the run proceeds in a goroutine.
func (r *Runner) RunBackup(ctx context.Context, trigger string) error {
	if !r.mu.TryLock() {
		return fmt.Errorf("busy")
	}
	r.running = true
	go func() {
		defer func() {
			r.mu.Lock()
			r.running = false
			r.mu.Unlock()
		}()
		_ = r.execBackup(ctx, trigger)
	}()
	return nil
}

func (r *Runner) execBackup(ctx context.Context, trigger string) error {
	id, _ := r.store.StartRun(trigger)
	ts := time.Now().UTC().Format("20060102T150405Z")
	logPath := filepath.Join(r.logDir, fmt.Sprintf("backup-%s.log", ts))
	r.store.SetLog(id, logPath)
	lf, _ := os.Create(logPath)
	if lf != nil {
		defer lf.Close()
	}
	cmd := lowPriorityCmd(ctx, filepath.Join(r.scripts, "home-backup.sh"))
	cmd.Env = append(os.Environ(), "LOG_FILE_OVERRIDE="+logPath)
	if lf != nil {
		cmd.Stdout = lf
		cmd.Stderr = lf
	}
	err := cmd.Run()
	exit := 0
	if ee, ok := err.(*exec.ExitError); ok {
		exit = ee.ExitCode()
	}
	sum, _ := os.ReadFile("/state/last-summary.json")
	dbsum, _ := os.ReadFile("/state/last-db-summary.json")
	var dataAdded int64
	var snap string
	if bs, e := ParseBackupSummary(sum); e == nil {
		dataAdded = bs.DataAdded
		snap = bs.SnapshotID
	}
	status := "ok"
	emsg := ""
	if err != nil {
		status = "failed"
		emsg = Redact(err.Error())
	}
	r.store.FinishRun(id, status, exit, dataAdded, snap, string(dbsum), emsg)
	return err
}

// RunRestore runs the restore script synchronously (called by API after re-auth + path check).
func (r *Runner) RunRestore(ctx context.Context, snap, target string, includes []string) error {
	if !r.mu.TryLock() {
		return fmt.Errorf("busy")
	}
	defer func() {
		r.mu.Lock()
		r.running = false
		r.mu.Unlock()
	}()
	r.running = true
	args := []string{"restore", snap, target}
	args = append(args, includes...)
	cmd := lowPriorityCmd(ctx, filepath.Join(r.scripts, "home-restore.sh"), args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%v: %s", err, Redact(string(out)))
	}
	return nil
}

// lowPriorityCmd wraps a command in ionice/nice and sets a new process group
// so the whole tree (including restic) can be signalled on shutdown.
func lowPriorityCmd(ctx context.Context, script string, args ...string) *exec.Cmd {
	full := append([]string{"-c3", "nice", "-n19", script}, args...)
	cmd := exec.CommandContext(ctx, "ionice", full...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process != nil {
			// SIGINT the whole group so restic releases its lock cleanly
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGINT)
		}
		return nil
	}
	cmd.WaitDelay = 20 * time.Second
	return cmd
}
