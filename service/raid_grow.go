package service

import (
	"errors"
	"fmt"
	"os"

	"github.com/ReCasaOS/CasaOS-LocalStorage/model"
)

// Changing a RAID 1 of two disks to a RAID 5 of three, and then letting the filesystem use the room. Both go through mdadm and resize2fs. The array stays usable all
// along: as a RAID 5 of two disks it is the same mirror, and the reshape onto the third disk runs in the background for as long as the disks take.

var (
	ErrRAIDNotMirror  = errors.New("only a RAID 1 of two disks can be changed to a RAID 5")
	ErrRAIDNotSettled = errors.New("the array must be healthy and doing nothing else: wait until it has finished synchronizing")
	ErrRAIDFilesystem = errors.New("only an ext2, ext3 or ext4 filesystem can be grown here: grow it with its own tool")
)

// raidBitmapMinimum is the size from which mdadm gives an array a write-intent bitmap by itself (100 GiB): a smaller array is given none again.
const raidBitmapMinimum = 100 << 30

// RAIDToRAID5 changes the RAID 1 array of two disks to a RAID 5 of three, the third being disk (what RAIDCandidate gave, wiped here). The four steps leave the array
// redundant after each: the write-intent bitmap, which a reshape refuses, is taken off; the mirror becomes a RAID 5 of two disks; the disk is added; the array is
// asked for three disks, which starts the reshape (backup is where mdadm puts the part of the data it cannot move without a copy; not on the array). When a step
// fails the array is brought back to a RAID 1 with its bitmap, and the error says whether that worked.
func RAIDToRAID5(array RAIDArray, disk model.LSBLKModel, backup string) error {
	if array.Level != "raid1" || array.Disks != 2 {
		return ErrRAIDNotMirror
	}
	if array.State != "ok" || array.Action != "idle" {
		return ErrRAIDNotSettled
	}
	if err := ensureMDADM(); err != nil {
		return err
	}
	if err := wipeRAIDDisk(disk); err != nil {
		return err
	}
	_ = os.Remove(backup) // the copy of an earlier attempt: mdadm will not take over a file that exists
	if array.Bitmap {
		if err := raidRun("mdadm", "--grow", array.Path, "--bitmap=none"); err != nil {
			return fmt.Errorf("taking the bitmap off %s: %w", array.Path, err)
		}
	}
	if err := raidRun("mdadm", "--grow", array.Path, "--level=5"); err != nil {
		return withBitmapBack(array, fmt.Errorf("changing %s to RAID 5: %w", array.Path, err))
	}
	if err := raidRun("mdadm", "--manage", array.Path, "--add", disk.Path); err != nil {
		return backToMirror(array, fmt.Errorf("adding %s to %s: %w", disk.Path, array.Path, err))
	}
	if err := raidRun("mdadm", "--grow", array.Path, "--raid-devices=3", "--backup-file="+backup); err != nil {
		return backToMirror(array, fmt.Errorf("reshaping %s onto %s: %w", array.Path, disk.Path, err))
	}
	return nil
}

// backToMirror undoes the change of level, then gives the bitmap back; err is what failed, and what could not be undone is added to it.
func backToMirror(array RAIDArray, err error) error {
	if undo := raidRun("mdadm", "--grow", array.Path, "--level=1"); undo != nil {
		return fmt.Errorf("%w; and it could not be changed back to RAID 1: %v", err, undo)
	}
	return withBitmapBack(array, fmt.Errorf("%w; %s is a RAID 1 again", err, array.Path))
}

func withBitmapBack(array RAIDArray, err error) error {
	if !array.Bitmap {
		return err
	}
	if undo := raidRun("mdadm", "--grow", array.Path, "--bitmap=internal"); undo != nil {
		return fmt.Errorf("%w; and its bitmap could not be put back: %v", err, undo)
	}
	return err
}

// GrowRAIDFilesystem makes the filesystem on the array, mounted or not, take the room the array has now (after a reshape), and gives a RAID 5 or 6 that is big enough
// the bitmap that mdadm would have given it. It does nothing the array is not ready for: ErrRAIDNotSettled while it reshapes. A reshape's backup file is not needed
// any more and goes. fsType is what lsblk says the filesystem is.
func GrowRAIDFilesystem(array RAIDArray, fsType, backup string) error {
	if array.State != "ok" || array.Action != "idle" {
		return ErrRAIDNotSettled
	}
	switch fsType {
	case "ext2", "ext3", "ext4":
	default:
		return ErrRAIDFilesystem
	}
	if err := raidRun("resize2fs", array.Path); err != nil {
		return fmt.Errorf("growing the filesystem on %s: %w", array.Path, err)
	}
	if (array.Level == "raid5" || array.Level == "raid6") && !array.Bitmap && array.Size >= raidBitmapMinimum {
		if err := raidRun("mdadm", "--grow", array.Path, "--bitmap=internal"); err != nil {
			return fmt.Errorf("the filesystem is grown, but %s could not be given its bitmap: %w", array.Path, err)
		}
	}
	_ = os.Remove(backup)
	return nil
}
