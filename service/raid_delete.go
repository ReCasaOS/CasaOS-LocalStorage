package service

import (
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/ReCasaOS/CasaOS-LocalStorage/model"
)

// Deleting a software RAID array: its filesystem is unmounted by the caller, the array is stopped and each disk loses its RAID header, so that the disks read blank and
// may be given to another array. Everything on the array is lost.

var (
	ErrRAIDSystem  = errors.New("the system is on this array: it cannot be deleted")
	ErrRAIDPooled  = errors.New("this array is part of the merged storage or of the SnapRAID protection: take it out of there first")
	ErrRAIDLayered = errors.New("volumes are stacked on this array (LVM, encryption): take them down first")
	ErrRAIDInUse   = errors.New("the array is in use: stop what reads or writes it (an app, a share), then try again")
)

// raidStopTries is how many times the array is asked to stop: md may keep it open a moment after its filesystem is unmounted (udev reads it).
const raidStopTries = 5

// RAIDDeletable says why the array whose volume is blk (as lsblk shows it) may not be deleted: the system is on it, it is a disk of the merged storage or of SnapRAID
// (taken, their mount points and volume UUIDs), or volumes are stacked on it, which this does not take down.
func RAIDDeletable(blk model.LSBLKModel, taken []string) error {
	if WalkDisk(blk, 5, func(b model.LSBLKModel) bool { return b.MountPoint == "/" }) != nil {
		return ErrRAIDSystem
	}
	if blk.UUID != "" && slices.Contains(taken, blk.UUID) ||
		WalkDisk(blk, 5, func(b model.LSBLKModel) bool { return b.MountPoint != "" && slices.Contains(taken, b.MountPoint) }) != nil {
		return ErrRAIDPooled
	}
	for _, child := range blk.Children {
		for _, below := range child.Children {
			if WalkDisk(below, 5, func(b model.LSBLKModel) bool { return b.MountPoint != "" }) != nil {
				return ErrRAIDLayered
			}
		}
	}
	return nil
}

// RAIDVolumes are the parts of the array's volume that are mounted: the volume itself, or the partitions on it.
func RAIDVolumes(blk model.LSBLKModel) []model.LSBLKModel {
	var volumes []model.LSBLKModel
	if blk.MountPoint != "" {
		volumes = append(volumes, blk)
	}
	for _, child := range blk.Children {
		if child.MountPoint != "" {
			volumes = append(volumes, child)
		}
	}
	return volumes
}

// DeleteRAIDArray stops the array and erases the RAID header of each member; a member that is a whole disk is wiped too. A failed member that is no longer there is let
// go. The array's filesystem has to be unmounted first: a stop that is refused answers ErrRAIDInUse, and nothing is erased.
func DeleteRAIDArray(array RAIDArray) error {
	if err := ensureMDADM(); err != nil {
		return err
	}
	var err error
	for try := 0; try < raidStopTries; try++ {
		if try > 0 {
			time.Sleep(raidRetryWait)
		}
		if err = raidRun("mdadm", "--stop", array.Path); err == nil {
			break
		}
	}
	if err != nil {
		return fmt.Errorf("%w: %v", ErrRAIDInUse, err)
	}
	for _, m := range array.Members {
		dev := "/dev/" + m.Name
		if err := raidRun("mdadm", "--zero-superblock", dev); err != nil {
			if m.State == "faulty" {
				continue
			}
			return fmt.Errorf("erasing %s: %w", dev, err)
		}
		if m.Name == m.Disk {
			if err := raidRun("wipefs", "--all", dev); err != nil {
				return fmt.Errorf("wiping %s: %w", dev, err)
			}
		}
	}
	return nil
}
