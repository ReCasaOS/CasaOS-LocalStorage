package service

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/ReCasaOS/CasaOS-Common/utils/logger"
	"github.com/ReCasaOS/CasaOS-LocalStorage/common"
	"go.uber.org/zap"
)

// What SnapRAID is asked to do: diff (what changed since the last sync), sync (protect it), scrub (read the disks and the parity back, to find what rotted), fix
// (put back what a data disk lost). One at a time: they all read every disk, and sync and fix write.

// SnapRAIDOptions are what an action can be told besides its name.
type SnapRAIDOptions struct {
	// Force: sync although more files than MaxRemoved were removed.
	Force bool
	// Full: scrub all of the array, not the slice of it that is the oldest.
	Full bool
	// Disk and Confirm: fix this data disk only (empty for all of them); Confirm repeats it ("all" for all of them), because fix puts files back as they were at the
	// last sync, over what was written since.
	Disk    string
	Confirm string
}

var (
	snapraidJob  *SnapRAIDJob // under snapraidMu
	snapraidStop context.CancelFunc

	snapraidProgress = regexp.MustCompile(`^\s*(\d{1,3})%`)
	snapraidCount    = regexp.MustCompile(`^\s*(\d+) (equal|added|removed|updated|moved|copied|restored)$`)
	snapraidNoise    = regexp.MustCompile(`^(Saving state|Verif|Self test|Loading state|Using |Initializing|Resizing|Selecting|Scanning|Scanned|Comparing|Searching|WARNING! (Physical offsets|UUID is unsupported))|\|`)
)

// snapraidExec runs snapraid with args and gives each line it prints (a progress line ends with \r, not \n) to line. It answers the exit code. A var for the tests.
// Stopping the context sends an interrupt: snapraid saves where it was and ends by itself, which a kill would not let it.
var snapraidExec = func(ctx context.Context, args []string, line func(string)) (int, error) {
	cmd := exec.CommandContext(ctx, "snapraid", args...)
	cmd.Cancel = func() error { return cmd.Process.Signal(os.Interrupt) }
	cmd.WaitDelay = 10 * time.Minute
	reader, writer := io.Pipe()
	cmd.Stdout, cmd.Stderr = writer, writer
	if err := cmd.Start(); err != nil {
		return -1, err
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		scanner := bufio.NewScanner(reader)
		scanner.Split(splitSnapRAIDLines)
		for scanner.Scan() {
			line(scanner.Text())
		}
		_, _ = io.Copy(io.Discard, reader)
	}()
	err := cmd.Wait()
	_ = writer.Close()
	<-done
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exit.ExitCode(), nil
	}
	if err != nil {
		return -1, err
	}
	return 0, nil
}

func splitSnapRAIDLines(data []byte, atEOF bool) (int, []byte, error) {
	if i := bytes.IndexAny(data, "\r\n"); i >= 0 {
		return i + 1, data[:i], nil
	}
	if atEOF && len(data) > 0 {
		return len(data), data, nil
	}
	return 0, nil, nil
}

// snapraidNotify tells the dashboard that an action ended, a var for the tests.
var snapraidNotify = func(action string, result SnapRAIDResult) {
	message := map[string]interface{}{"data": map[string]interface{}{"action": action, "ok": result.OK, "blocked": result.Blocked, "message": result.Message}}
	if err := MyService.Notify().SendNotify(common.ServiceName+":snapraid", message); err != nil {
		logger.Error("error when sending notification", zap.Error(err), zap.String("action", action))
	}
}

// snapraidReady checks what an action needs before anything starts: the setup, snapraid itself, every disk where it should be, and for fix the consent.
func snapraidReady(action string, opts SnapRAIDOptions) (SnapRAIDConfig, error) {
	switch action {
	case "diff", "sync", "scrub", "fix":
	default:
		return SnapRAIDConfig{}, ErrSnapRAIDAction
	}
	snapraidMu.Lock()
	f, ok := loadSnapRAID()
	snapraidMu.Unlock()
	if !ok {
		return SnapRAIDConfig{}, ErrSnapRAIDNotSet
	}
	cfg := f.Config
	if action == "fix" {
		known := opts.Disk == ""
		for _, d := range cfg.Data {
			known = known || d.Name == opts.Disk
		}
		if !known {
			return SnapRAIDConfig{}, ErrSnapRAIDNoDisk
		}
		want := opts.Disk
		if want == "" {
			want = "all"
		}
		if opts.Confirm != want {
			return SnapRAIDConfig{}, ErrSnapRAIDConsent
		}
	}
	if err := ensureSnapRAID(); err != nil {
		return SnapRAIDConfig{}, err
	}
	// A data disk that is not mounted is an empty folder to SnapRAID: every file it held would be taken for removed.
	if missing := missingSnapRAIDDisks(cfg); len(missing) > 0 {
		return SnapRAIDConfig{}, fmt.Errorf("%w: %s", ErrSnapRAIDNotMounted, strings.Join(missing, ", "))
	}
	return cfg, nil
}

func beginSnapRAIDJob(action string) (context.Context, SnapRAIDJob, error) {
	snapraidMu.Lock()
	defer snapraidMu.Unlock()
	if snapraidJob != nil {
		return nil, SnapRAIDJob{}, ErrSnapRAIDBusy
	}
	ctx, cancel := context.WithCancel(context.Background())
	snapraidJob = &SnapRAIDJob{Action: action, StartedAt: snapraidNow()}
	snapraidStop = cancel
	return ctx, *snapraidJob, nil
}

func endSnapRAIDJob() {
	snapraidMu.Lock()
	defer snapraidMu.Unlock()
	if snapraidStop != nil {
		snapraidStop()
	}
	snapraidJob, snapraidStop = nil, nil
}

// StopSnapRAID interrupts the action at work; snapraid saves where it was.
func StopSnapRAID() bool {
	snapraidMu.Lock()
	defer snapraidMu.Unlock()
	if snapraidStop == nil {
		return false
	}
	snapraidStop()
	return true
}

// StartSnapRAID starts an action in the background and answers at once; SnapRAIDStatus follows it, and the message bus tells when it ends.
func StartSnapRAID(action string, opts SnapRAIDOptions) (SnapRAIDJob, error) {
	cfg, err := snapraidReady(action, opts)
	if err != nil {
		return SnapRAIDJob{}, err
	}
	ctx, job, err := beginSnapRAIDJob(action)
	if err != nil {
		return SnapRAIDJob{}, err
	}
	go func() {
		defer endSnapRAIDJob()
		runSnapRAID(ctx, action, opts, cfg)
	}()
	return job, nil
}

// runSnapRAIDNow does the same and waits for the end.
func runSnapRAIDNow(action string, opts SnapRAIDOptions) (SnapRAIDResult, error) {
	cfg, err := snapraidReady(action, opts)
	if err != nil {
		return SnapRAIDResult{}, err
	}
	ctx, _, err := beginSnapRAIDJob(action)
	if err != nil {
		return SnapRAIDResult{}, err
	}
	defer endSnapRAIDJob()
	return runSnapRAID(ctx, action, opts, cfg), nil
}

// snapraidRun is one run of snapraid: the exit code, the end of what it said without the noise, and the counts of a diff if it gave them.
type snapraidRun struct {
	code int
	tail []string
	diff *SnapRAIDDiff
	err  error
}

func execSnapRAID(ctx context.Context, args ...string) snapraidRun {
	var r snapraidRun
	full := append([]string{"-c", snapraidConfPath()}, args...)
	r.code, r.err = snapraidExec(ctx, full, func(line string) {
		line = strings.TrimSpace(line)
		if m := snapraidProgress.FindStringSubmatch(line); m != nil {
			percent, _ := strconv.Atoi(m[1])
			snapraidMu.Lock()
			if snapraidJob != nil {
				snapraidJob.Percent = percent
			}
			snapraidMu.Unlock()
			return
		}
		if m := snapraidCount.FindStringSubmatch(line); m != nil {
			if r.diff == nil {
				r.diff = &SnapRAIDDiff{}
			}
			n, _ := strconv.Atoi(m[1])
			switch m[2] {
			case "equal":
				r.diff.Equal = n
			case "added":
				r.diff.Added = n
			case "removed":
				r.diff.Removed = n
			case "updated":
				r.diff.Updated = n
			case "moved":
				r.diff.Moved = n
			case "copied":
				r.diff.Copied = n
			case "restored":
				r.diff.Restored = n
			}
			return
		}
		if line == "" || snapraidNoise.MatchString(line) || strings.HasPrefix(line, "add ") || strings.HasPrefix(line, "remove ") || strings.HasPrefix(line, "update ") {
			return
		}
		r.tail = append(r.tail, line)
		if len(r.tail) > 5 {
			r.tail = r.tail[1:]
		}
	})
	return r
}

func (r snapraidRun) failure(ctx context.Context) string {
	switch {
	case ctx.Err() != nil:
		return "stopped"
	case r.err != nil:
		return r.err.Error()
	}
	message := strings.Join(r.tail, "\n")
	if len(message) > 600 {
		message = message[len(message)-600:]
	}
	if message == "" {
		message = "snapraid ended with code " + strconv.Itoa(r.code)
	}
	return message
}

// runSnapRAID does the action, keeps how it ended and tells the dashboard.
func runSnapRAID(ctx context.Context, action string, opts SnapRAIDOptions, cfg SnapRAIDConfig) SnapRAIDResult {
	result := SnapRAIDResult{At: snapraidNow()}
	if err := writeSnapRAIDConf(cfg); err != nil {
		result.Message = err.Error()
		return finishSnapRAID(action, result)
	}
	switch action {
	case "diff":
		// 2 is "there are differences", which is the answer, not a failure
		r := execSnapRAID(ctx, "diff")
		if r.err != nil || (r.code != 0 && r.code != 2) || r.diff == nil {
			result.Message = r.failure(ctx)
			break
		}
		result.OK, result.Diff = true, r.diff
	case "sync":
		d := execSnapRAID(ctx, "diff")
		if d.err != nil || (d.code != 0 && d.code != 2) || d.diff == nil {
			result.Message = d.failure(ctx)
			break
		}
		result.Diff = d.diff
		if d.diff.Removed > cfg.MaxRemoved && !opts.Force {
			result.Blocked = true
			result.Message = fmt.Sprintf("%d files were removed since the last sync, more than the %d allowed: nothing was synced, so the parity still holds them. Look at what happened, then sync anyway.", d.diff.Removed, cfg.MaxRemoved)
			break
		}
		if r := execSnapRAID(ctx, "sync"); r.err != nil || r.code != 0 {
			result.Message = r.failure(ctx)
			break
		}
		result.OK = true
	case "scrub":
		args := []string{"scrub"}
		if opts.Full {
			args = append(args, "-p", "100", "-o", "0")
		}
		if r := execSnapRAID(ctx, args...); r.err != nil || r.code != 0 {
			result.Message = r.failure(ctx)
			break
		}
		result.OK = true
	case "fix":
		args := []string{"fix"}
		if opts.Disk != "" {
			args = []string{"-d", opts.Disk, "fix"}
		}
		if r := execSnapRAID(ctx, args...); r.err != nil || r.code != 0 {
			result.Message = r.failure(ctx)
			break
		}
		result.OK = true
	}
	return finishSnapRAID(action, result)
}

func finishSnapRAID(action string, result SnapRAIDResult) SnapRAIDResult {
	recordSnapRAID(action, result)
	snapraidNotify(action, result)
	return result
}

func recordSnapRAID(action string, result SnapRAIDResult) {
	snapraidMu.Lock()
	defer snapraidMu.Unlock()
	f, ok := loadSnapRAID()
	if !ok {
		return
	}
	f.Last[action] = result
	if err := saveSnapRAID(f); err != nil {
		logger.Error("error when keeping the result of SnapRAID", zap.Error(err), zap.String("action", action))
	}
}
