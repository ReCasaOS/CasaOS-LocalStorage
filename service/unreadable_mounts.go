package service

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"slices"
	"strings"
	"time"
)

// The mounts df cannot read: a dead NFS share ("Stale file handle"), a FUSE mount whose program died
// ("Transport endpoint is not connected"). Nothing said so: the storage list left them out, and until
// LocalStorage#30 it stopped answering altogether. The dashboard puts them in "Needs attention".

// UnreadableMount is a mount df complained about, with its complaint in English. An empty MountPoint is
// df itself not answering, which a hard NFS mount whose server is gone does.
type UnreadableMount struct {
	MountPoint string `json:"mount_point"`
	Error      string `json:"error"`
}

const dfTimeout = 15 * time.Second

// what root is refused on another user's FUSE mount (a desktop's /run/user/1000/doc): not a dead mount
var dfRefusals = []string{"Permission denied", "Operation not permitted"}

// dfComplaints runs df for its complaints alone; a var for the tests.
var dfComplaints = func(ctx context.Context) (string, error) {
	cmd := exec.CommandContext(ctx, "df", "-P")
	cmd.Env = append(os.Environ(), "LC_ALL=C") // the complaints in English whatever the language of the box
	cmd.WaitDelay = 2 * time.Second
	stderr := &cappedBuffer{max: 16384}
	cmd.Stderr = stderr
	err := cmd.Run()
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	var exit *exec.ExitError
	if err != nil && !errors.As(err, &exit) { // exit 1 is what df does when it complains
		return "", err
	}
	return stderr.String(), nil
}

// UnreadableMounts lists the mounts df cannot read, none on a healthy box.
func UnreadableMounts() ([]UnreadableMount, error) {
	ctx, cancel := context.WithTimeout(context.Background(), dfTimeout)
	defer cancel()
	stderr, err := dfComplaints(ctx)
	if errors.Is(err, context.DeadlineExceeded) {
		return []UnreadableMount{{Error: "df did not answer within 15 s"}}, nil
	}
	if err != nil {
		return nil, err
	}
	return parseDfComplaints(stderr), nil
}

// parseDfComplaints reads lines like "df: /media/fotos: Stale file handle"; df quotes a path that has
// spaces or quotes in it, shell style. The path is cut at the last ": ", the complaints having none.
func parseDfComplaints(stderr string) []UnreadableMount {
	mounts := []UnreadableMount{}
	for _, line := range strings.Split(stderr, "\n") {
		rest, ok := strings.CutPrefix(strings.TrimSpace(line), "df: ")
		cut := strings.LastIndex(rest, ": ")
		if !ok || cut <= 0 {
			continue
		}
		path, complaint := rest[:cut], rest[cut+2:]
		if slices.Contains(dfRefusals, complaint) {
			continue
		}
		if len(path) >= 2 && path[0] == '\'' && path[len(path)-1] == '\'' {
			path = strings.ReplaceAll(path[1:len(path)-1], `'\''`, `'`)
		}
		mounts = append(mounts, UnreadableMount{MountPoint: path, Error: complaint})
	}
	return mounts
}
