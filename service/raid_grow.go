package service

import (
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/ReCasaOS/CasaOS-LocalStorage/model"
)

// Changing a RAID 1 of two disks to a RAID 5 of three, and then letting the filesystem use the room. Both go through mdadm and resize2fs. The array stays usable all
// along: as a RAID 5 of two disks it is the same mirror, and the reshape onto the third disk runs in the background for as long as the disks take.

var (
	ErrRAIDNotMirror  = errors.New("only a RAID 1 of two or three disks can be changed to a RAID 5")
	ErrRAIDNotMember  = errors.New("that drive cannot be freed: only the last drive of the mirror can")
	ErrRAIDNotSettled = errors.New("the array must be healthy and doing nothing else: wait until it has finished synchronizing")
	ErrRAIDFilesystem = errors.New("only an ext2, ext3 or ext4 filesystem can be grown here: grow it with its own tool")
)

// raidBitmapMinimum is the size from which mdadm gives an array a write-intent bitmap by itself (100 GiB): a smaller array is given none again.
const raidBitmapMinimum = 100 << 30

// raidRetryWait is how long a drive that has just been failed is given before it is taken out again: md lets go of it a moment after, a var for the tests.
var raidRetryWait = time.Second

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
	return reshapeToRAID5(array, disk.Path, backup)
}

// reshapeToRAID5 does what is left once the disk to add is blank, on the RAID 1 of two disks array.
func reshapeToRAID5(array RAIDArray, diskPath, backup string) error {
	_ = os.Remove(backup) // the copy of an earlier attempt: mdadm will not take over a file that exists
	if array.Bitmap {
		if err := raidRun("mdadm", "--grow", array.Path, "--bitmap=none"); err != nil {
			return fmt.Errorf("taking the bitmap off %s: %w", array.Path, err)
		}
	}
	if err := raidRun("mdadm", "--grow", array.Path, "--level=5"); err != nil {
		return withBitmapBack(array, fmt.Errorf("changing %s to RAID 5: %w", array.Path, err))
	}
	if err := raidRun("mdadm", "--manage", array.Path, "--add", diskPath); err != nil {
		return backToMirror(array, fmt.Errorf("adding %s to %s: %w", diskPath, array.Path, err))
	}
	if err := raidRun("mdadm", "--grow", array.Path, "--raid-devices=3", "--backup-file="+backup); err != nil {
		return backToMirror(array, fmt.Errorf("reshaping %s onto %s: %w", array.Path, diskPath, err))
	}
	return nil
}

// RAIDLastMember is the drive a RAID 1 of three gives up to become a RAID 5: the active member in its last slot, the only one mdadm can take out when the mirror is
// cut down to two.
func RAIDLastMember(array RAIDArray) (RAIDMember, bool) {
	for _, m := range array.Members {
		if m.State == "active" && m.Slot == array.Disks-1 {
			return m, true
		}
	}
	return RAIDMember{}, false
}

// RAIDFreeableDisk is the disk at path, the one that holds the last drive of the RAID 1 of three disks array, if the array may give it up: whole and idle, made of
// three active drives and nothing else, and the disk holds nothing but that drive and the array on it (no other partition, not the system's). It answers with the disk
// and the member on it. A mirror of three survives two failed drives, a RAID 5 only one: the person is told before this is asked.
func RAIDFreeableDisk(disks []model.LSBLKModel, array RAIDArray, path string) (model.LSBLKModel, RAIDMember, error) {
	if array.Level != "raid1" || array.Disks != 3 || len(array.Members) != 3 {
		return model.LSBLKModel{}, RAIDMember{}, ErrRAIDNotMirror
	}
	if array.State != "ok" || array.Action != "idle" {
		return model.LSBLKModel{}, RAIDMember{}, ErrRAIDNotSettled
	}
	member, ok := RAIDLastMember(array)
	if !ok {
		return model.LSBLKModel{}, RAIDMember{}, ErrRAIDNotMember
	}
	if path != "/dev/"+member.Disk {
		return model.LSBLKModel{}, RAIDMember{}, fmt.Errorf("%w: it is %s", ErrRAIDNotMember, member.Disk)
	}
	for _, d := range disks {
		if d.Path != path {
			continue
		}
		system := WalkDisk(d, 5, func(blk model.LSBLKModel) bool { return blk.MountPoint == "/" }) != nil
		if system || d.Type != "disk" || !IsDiskSupported(d) || !holdsOnly(d, "/dev/"+member.Name, array.Path) {
			return model.LSBLKModel{}, RAIDMember{}, ErrRAIDDiskNotFree
		}
		return d, member, nil
	}
	return model.LSBLKModel{}, RAIDMember{}, ErrRAIDDiskNotFree
}

// holdsOnly says whether what is on the disk is the array's member and the array on it, and nothing else.
func holdsOnly(disk model.LSBLKModel, memberPath, arrayPath string) bool {
	for _, c := range disk.Children {
		switch c.Path {
		case arrayPath:
		case memberPath:
			if !holdsOnly(c, memberPath, arrayPath) {
				return false
			}
		default:
			return false
		}
	}
	return true
}

// RAIDTrioToRAID5 changes the RAID 1 of three disks to a RAID 5 of three without a fourth drive: the member in the last slot (RAIDFreeableDisk) is taken out, the
// mirror is cut down to two, the drive is erased and goes back in as the third of the RAID 5 (reshapeToRAID5). The array has two copies of its data all along
// and the same drives at the end. Where it fails it says what state the array is left in.
func RAIDTrioToRAID5(array RAIDArray, disk model.LSBLKModel, member RAIDMember, backup string) error {
	if err := ensureMDADM(); err != nil {
		return err
	}
	dev := "/dev/" + member.Name
	if err := raidRun("mdadm", "--manage", array.Path, "--fail", dev); err != nil {
		return fmt.Errorf("taking %s out of %s: %w", dev, array.Path, err)
	}
	if err := removeMember(array.Path, dev); err != nil {
		return putBack(array, dev, fmt.Errorf("taking %s out of %s: %w", dev, array.Path, err))
	}
	if err := raidRun("mdadm", "--grow", array.Path, "--raid-devices=2"); err != nil {
		return putBack(array, dev, fmt.Errorf("cutting %s down to two drives: %w", array.Path, err))
	}
	two := array
	two.Disks = 2
	two.Members = nil
	if err := wipeFreedMember(member, disk); err != nil {
		return fmt.Errorf("%w; %s is a RAID 1 of two drives now, and %s is out of it", err, array.Path, dev)
	}
	return reshapeToRAID5(two, disk.Path, backup)
}

// removeMember takes a drive that has just been failed out of the array, trying again for a few seconds: md lets go of it a moment after the failure.
func removeMember(array, dev string) error {
	var err error
	for i := 0; i < 10; i++ {
		if err = raidRun("mdadm", "--manage", array, "--remove", dev); err == nil {
			return nil
		}
		time.Sleep(raidRetryWait)
	}
	return err
}

// putBack puts a drive that was taken out back in the mirror, and tells err with what became of it.
func putBack(array RAIDArray, dev string, err error) error {
	if undo := raidRun("mdadm", "--manage", array.Path, "--re-add", dev); undo != nil {
		return fmt.Errorf("%w; and %s could not be put back in %s: %v", err, dev, array.Path, undo)
	}
	return fmt.Errorf("%w; %s is back in %s", err, dev, array.Path)
}

// wipeFreedMember erases the RAID header of the drive that came out, and the signatures of its partition and of its disk: mdadm takes the disk whole.
func wipeFreedMember(member RAIDMember, disk model.LSBLKModel) error {
	dev := "/dev/" + member.Name
	if err := raidRun("mdadm", "--zero-superblock", dev); err != nil {
		return fmt.Errorf("erasing the RAID header of %s: %w", dev, err)
	}
	if member.Name != member.Disk {
		if err := raidRun("wipefs", "--all", dev); err != nil {
			return fmt.Errorf("wiping %s: %w", dev, err)
		}
	}
	if err := raidRun("wipefs", "--all", disk.Path); err != nil {
		return fmt.Errorf("wiping %s: %w", disk.Path, err)
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
