package v2

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/moby/sys/mountinfo"
)

type fixedMounts []string

func (m fixedMounts) GetMounts(f mountinfo.FilterFunc) ([]*mountinfo.Info, error) {
	var kept []*mountinfo.Info
	for _, at := range m {
		info := &mountinfo.Info{Mountpoint: at}
		if skip, _ := f(info); !skip {
			kept = append(kept, info)
		}
	}
	return kept, nil
}

func strayMountPoint(t *testing.T) string {
	t.Helper()
	mountPoint := filepath.Join(t.TempDir(), "DATA")
	for _, dir := range []string{"AppData/homeassistant", "Media"} {
		if err := os.MkdirAll(filepath.Join(mountPoint, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(mountPoint, "AppData/homeassistant/config.yaml"), []byte("fresh"), 0o644); err != nil {
		t.Fatal(err)
	}
	return mountPoint
}

func names(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, entry := range entries {
		out = append(out, entry.Name())
	}
	return out
}

// What apps wrote to the bare directory is moved whole, and the mount point is
// left empty for the merge (CasaOS#207).
func TestSetAsideStrayEntriesMovesEverything(t *testing.T) {
	mountPoint := strayMountPoint(t)
	root := filepath.Join(t.TempDir(), "set-aside")

	destination, err := setAsideStrayEntries(mountPoint, root, time.Date(2026, 11, 28, 19, 23, 43, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(destination) != root || !strings.HasSuffix(destination, "DATA-20261128-192343") {
		t.Errorf("destination %q", destination)
	}
	if left := names(t, mountPoint); len(left) != 0 {
		t.Errorf("left in the mount point: %v", left)
	}
	if got := strings.Join(names(t, destination), ","); got != "AppData,Media,"+setAsideNote {
		t.Errorf("set aside: %s", got)
	}
	content, err := os.ReadFile(filepath.Join(destination, "AppData/homeassistant/config.yaml"))
	if err != nil || string(content) != "fresh" {
		t.Errorf("the file did not come along unchanged: %q, %v", content, err)
	}
}

func TestSetAsideStrayEntriesEmptyMountPoint(t *testing.T) {
	root := filepath.Join(t.TempDir(), "set-aside")

	destination, err := setAsideStrayEntries(t.TempDir(), root, time.Now())
	if err != nil || destination != "" {
		t.Errorf("got %q, %v", destination, err)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Errorf("an empty mount point left a folder behind: %v", err)
	}
}

// A destination that is already there is never written into.
func TestSetAsideStrayEntriesKeepsAnExistingDestination(t *testing.T) {
	mountPoint := strayMountPoint(t)
	root := filepath.Join(t.TempDir(), "set-aside")
	now := time.Date(2026, 11, 28, 19, 23, 43, 0, time.UTC)

	destination, err := setAsideStrayEntries(mountPoint, root, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(mountPoint, "AppData"), 0o755); err != nil {
		t.Fatal(err)
	}

	if again, err := setAsideStrayEntries(mountPoint, root, now); err == nil {
		t.Fatalf("moved into %s, which already existed (first %s)", again, destination)
	}
	if got := strings.Join(names(t, mountPoint), ","); got != "AppData" {
		t.Errorf("the mount point was touched: %s", got)
	}
}

// Nothing is moved around a mount: a USB drive under /DATA, a leftover bind
// mount, or the mount point itself still being mounted.
func TestSetAsideForRestoreRefusesMountsBelow(t *testing.T) {
	mountPoint := strayMountPoint(t)
	setAsideRoot = filepath.Join(t.TempDir(), "set-aside")

	for _, mounted := range []string{mountPoint, filepath.Join(mountPoint, "AppData"), filepath.Join(mountPoint, "USB_Storage_x")} {
		service := NewLocalStorageService(nil, fixedMounts{string(filepath.Separator), mountPoint + "-other", mounted})

		destination, err := service.setAsideForRestoreLocked(mountPoint)
		if !errors.Is(err, ErrMountsBelowMountPoint) || destination != "" {
			t.Errorf("%s mounted: got %q, %v", mounted, destination, err)
		}
	}

	// and without mount information, nothing is known, so nothing is moved
	if destination, err := NewLocalStorageService(nil, nil).setAsideForRestoreLocked(mountPoint); err == nil || destination != "" {
		t.Errorf("no mount information: got %q, %v", destination, err)
	}

	if got := strings.Join(names(t, mountPoint), ","); got != "AppData,Media" {
		t.Errorf("the mount point was touched: %s", got)
	}
	if _, err := os.Stat(setAsideRoot); !os.IsNotExist(err) {
		t.Errorf("a refusal left a folder behind: %v", err)
	}
}

// Something that refills the mount point faster than the merge is mounted
// does not fill the disk with dated folders.
func TestSetAsideForRestoreStopsAfterAFewTimes(t *testing.T) {
	mountPoint := filepath.Join(t.TempDir(), "DATA")
	setAsideRoot = filepath.Join(t.TempDir(), "set-aside")
	service := NewLocalStorageService(nil, fixedMounts{string(filepath.Separator), mountPoint + "-other"})

	for i := 0; i < maxSetAsidesPerMountPoint+2; i++ {
		if err := os.MkdirAll(filepath.Join(mountPoint, "AppData"), 0o755); err != nil {
			t.Fatal(err)
		}
		destination, err := service.setAsideForRestoreLocked(mountPoint)
		if i < maxSetAsidesPerMountPoint {
			if err != nil || destination == "" {
				t.Fatalf("pass %d: got %q, %v", i, destination, err)
			}
			// the folder is dated to the second: make room for the next pass
			if err := os.Rename(destination, fmt.Sprintf("%s-%d", destination, i)); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err == nil || destination != "" {
			t.Fatalf("pass %d: moved again: %q", i, destination)
		}
	}

	if got := len(names(t, setAsideRoot)); got != maxSetAsidesPerMountPoint {
		t.Errorf("%d dated folders, want %d", got, maxSetAsidesPerMountPoint)
	}
	if got := strings.Join(names(t, mountPoint), ","); got != "AppData" {
		t.Errorf("after the last refusal the mount point holds: %s", got)
	}
}

// Stopping the service, as every update does, must leave the mergerfs process
// of /DATA alive (CasaOS#207).
func TestUnitsLeaveTheMergeAloneWhenStopped(t *testing.T) {
	for _, unit := range []string{"casaos-local-storage.service", "casaos-local-storage-first.service"} {
		content, err := os.ReadFile(filepath.Join("..", "..", "build", "sysroot", "usr", "lib", "systemd", "system", unit))
		if err != nil {
			t.Fatal(err)
		}
		service := strings.SplitN(strings.ReplaceAll(string(content), "\r\n", "\n"), "[Service]\n", 2)
		if len(service) != 2 || !strings.Contains("\n"+strings.SplitN(service[1], "\n[", 2)[0]+"\n", "\nKillMode=process\n") {
			t.Errorf("%s: no KillMode=process in its [Service] section", unit)
		}
	}
}
