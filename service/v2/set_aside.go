package v2

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ReCasaOS/CasaOS-Common/utils/constants"
	"github.com/moby/sys/mountinfo"
)

// maxSetAsidesPerMountPoint bounds how often one run of the service moves the
// contents of a merge mount point away. Something that writes there faster
// than the merge can be mounted would otherwise fill the disk with dated
// folders, one every 30 seconds.
const maxSetAsidesPerMountPoint = 3

const setAsideNote = "WHY-THIS-FOLDER-EXISTS.txt"

var ErrMountsBelowMountPoint = errors.New("something is mounted at or below the mount point")

// setAsideRoot is where the stray contents of a merge mount point are moved.
// It is outside every branch of the merge (the system branch is
// constants.DefaultFilePath), so what is set aside never shows up in /DATA.
var setAsideRoot = filepath.Join(constants.DefaultDataPath, "set-aside")

// setAsideStrayEntries moves everything in mountPoint, which must not be
// mounted, into a new dated folder under root, and returns that folder. With
// nothing to move it returns "".
//
// A merge that went away under running apps (CasaOS#207) leaves them writing
// to the bare directory: Docker recreates the folders of their volumes there.
// Those folders then kept the merge from ever being mounted again, with the
// real data unreachable behind them. They are moved, never copied and never
// deleted: a rename that cannot be done (another filesystem) is an error, and
// what was already moved stays where the error says.
func setAsideStrayEntries(mountPoint string, root string, now time.Time) (string, error) {
	entries, err := os.ReadDir(mountPoint)
	if err != nil {
		return "", err
	}
	if len(entries) == 0 {
		return "", nil
	}

	name := strings.Trim(strings.ReplaceAll(filepath.Clean(mountPoint), string(filepath.Separator), "_"), "_")
	destination := filepath.Join(root, name+"-"+now.UTC().Format("20060102-150405"))
	if _, err := os.Lstat(destination); err == nil {
		return "", fmt.Errorf("%s already exists", destination)
	} else if !os.IsNotExist(err) {
		return "", err
	}
	if err := os.MkdirAll(destination, 0o700); err != nil {
		return "", err
	}

	for _, entry := range entries {
		from := filepath.Join(mountPoint, entry.Name())
		if err := os.Rename(from, filepath.Join(destination, entry.Name())); err != nil {
			return destination, fmt.Errorf("move %s to %s: %w", from, destination, err)
		}
	}

	note := fmt.Sprintf("These files were found in %s on %s while the storage merge was not mounted there.\n"+
		"They were written to the system disk by apps that kept running without the merge,\n"+
		"and they kept the merge from being mounted. They were moved here, unchanged, so the\n"+
		"merge could come back. The data of your apps is on the merge, not here: look at what\n"+
		"is here before deleting it.\n", mountPoint, now.UTC().Format(time.RFC3339))
	// the note is a courtesy: the move is done whether or not it can be written
	_ = os.WriteFile(filepath.Join(destination, setAsideNote), []byte(note), 0o600)

	return destination, nil
}

// mountsAtOrBelow lists what is mounted at mountPoint or under it.
func (s *LocalStorageService) mountsAtOrBelow(mountPoint string) ([]*mountinfo.Info, error) {
	if s._mountinfo == nil {
		return nil, errors.New("mount information is unavailable")
	}
	mountPoint = filepath.Clean(mountPoint)
	return s._mountinfo.GetMounts(func(info *mountinfo.Info) (skip bool, stop bool) {
		at := filepath.Clean(info.Mountpoint)
		return at != mountPoint && !strings.HasPrefix(at, mountPoint+string(filepath.Separator)), false
	})
}

// setAsideForRestoreLocked clears the mount point of a persisted merge whose
// restore just failed because the mount point is not empty. It moves nothing
// when anything is mounted there or below (a USB drive under /DATA, a
// leftover bind mount): renaming around a mount is not something to try on
// someone's data.
func (s *LocalStorageService) setAsideForRestoreLocked(mountPoint string) (string, error) {
	if s._setAsides[mountPoint] >= maxSetAsidesPerMountPoint {
		return "", fmt.Errorf("the contents of %s were already set aside %d times since the service started", mountPoint, maxSetAsidesPerMountPoint)
	}

	mounts, err := s.mountsAtOrBelow(mountPoint)
	if err != nil {
		return "", err
	}
	if len(mounts) > 0 {
		return "", fmt.Errorf("%w: %s", ErrMountsBelowMountPoint, mounts[0].Mountpoint)
	}

	s._setAsides[mountPoint]++
	return setAsideStrayEntries(mountPoint, setAsideRoot, time.Now())
}
