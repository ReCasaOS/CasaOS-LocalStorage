package service

import (
	"errors"
	"fmt"
	"os/exec"

	"github.com/ReCasaOS/CasaOS-LocalStorage/model"
	"github.com/ReCasaOS/CasaOS-LocalStorage/pkg/utils/command"
)

// Repairing a software RAID array: a blank disk added where one is missing or
// failed, and a failed disk taken out. Both go through mdadm; nothing else is
// changed (no array grown or stopped here; raid_create.go makes one).

var (
	ErrNoRAIDArray      = errors.New("no such RAID array")
	ErrRAIDNotRunning   = errors.New("the array does not run: start it before you repair it")
	ErrRAIDWhole        = errors.New("the array lacks no disk")
	ErrRAIDNotFaulty    = errors.New("only a failed disk can be taken out of an array")
	ErrRAIDDiskNotFree  = errors.New("the disk is not a blank disk of this box: it is the system disk, holds something, or is not supported")
	ErrRAIDDiskTooSmall = errors.New("the disk is smaller than the other disks of the array")
	ErrNoMDADM          = errors.New("mdadm is not installed and could not be installed (sudo apt install mdadm)")
)

// raidRun runs a command for the repair, a var for the tests.
var raidRun = func(name string, args ...string) error {
	_, err := command.ExecuteCommand(name, args...)
	return err
}

// hasMDADM says whether mdadm can be run, a var for the tests.
var hasMDADM = func() bool {
	_, err := exec.LookPath("mdadm")
	return err == nil
}

// ensureMDADM installs mdadm with apt when the box lacks it: whoever asks for an array has asked for it, and being told to run apt at a prompt is the command line
// that all this is meant to spare. Only Debian and Ubuntu have apt-get; anywhere else the answer is ErrNoMDADM.
func ensureMDADM() error {
	if hasMDADM() {
		return nil
	}
	// noninteractive: mdadm's package asks where to mail alerts, and nobody is there to answer
	err := raidRun("env", "DEBIAN_FRONTEND=noninteractive", "apt-get", "-o", "DPkg::Lock::Timeout=120", "install", "-y", "-q", "--no-install-recommends", "mdadm")
	if err != nil {
		return fmt.Errorf("%w: %v", ErrNoMDADM, err)
	}
	if !hasMDADM() {
		return ErrNoMDADM
	}
	return nil
}

// RAIDArrayNamed is the array called name (md0), as RAIDArrays reads it.
func RAIDArrayNamed(sys, name string) (RAIDArray, error) {
	for _, a := range RAIDArrays(sys) {
		if a.Name == name {
			return a, nil
		}
	}
	return RAIDArray{}, ErrNoRAIDArray
}

// RAIDCandidate is the disk at path, if it may join an array whose members
// hold size bytes each: a whole disk of this box, not the system's, holding
// nothing (DiskInUse), of a supported kind, and big enough.
func RAIDCandidate(disks []model.LSBLKModel, path string, size int64) (model.LSBLKModel, error) {
	for _, d := range disks {
		if d.Path != path {
			continue
		}
		system := WalkDisk(d, 5, func(blk model.LSBLKModel) bool { return blk.MountPoint == "/" }) != nil
		if system || DiskInUse(d) || !IsDiskSupported(d) || d.Type != "disk" {
			return model.LSBLKModel{}, ErrRAIDDiskNotFree
		}
		if size > 0 && d.Size < uint64(size) {
			return model.LSBLKModel{}, ErrRAIDDiskTooSmall
		}
		return d, nil
	}
	return model.LSBLKModel{}, ErrRAIDDiskNotFree
}

// AddRAIDDisk wipes disk (what RAIDCandidate gave) and adds it to the array,
// which rebuilds onto it; with no disk missing but a failed one still in the
// array, it waits there as a spare until that one is taken out.
func AddRAIDDisk(array RAIDArray, disk model.LSBLKModel) error {
	if array.State == "inactive" {
		return ErrRAIDNotRunning
	}
	if array.Missing == 0 && !hasFaulty(array) {
		return ErrRAIDWhole
	}
	if err := ensureMDADM(); err != nil {
		return err
	}
	if err := wipeRAIDDisk(disk); err != nil {
		return err
	}
	if err := raidRun("mdadm", "--manage", array.Path, "--add", disk.Path); err != nil {
		return fmt.Errorf("adding %s to %s: %w", disk.Path, array.Path, err)
	}
	return nil
}

// RemoveRAIDDisk takes the failed member (sdc1) out of the array.
func RemoveRAIDDisk(array RAIDArray, member string) error {
	found := false
	for _, m := range array.Members {
		if m.Name == member && m.State == "faulty" {
			found = true
		}
	}
	if !found {
		return ErrRAIDNotFaulty
	}
	if err := ensureMDADM(); err != nil {
		return err
	}
	if err := raidRun("mdadm", "--manage", array.Path, "--remove", "/dev/"+member); err != nil {
		return fmt.Errorf("taking %s out of %s: %w", member, array.Path, err)
	}
	return nil
}

// wipeRAIDDisk erases the signatures of a disk and of its partitions, old ones first: mdadm takes the disk whole.
func wipeRAIDDisk(disk model.LSBLKModel) error {
	for _, part := range disk.Children {
		if err := raidRun("wipefs", "--all", part.Path); err != nil {
			return fmt.Errorf("wiping %s: %w", part.Path, err)
		}
	}
	if err := raidRun("wipefs", "--all", disk.Path); err != nil {
		return fmt.Errorf("wiping %s: %w", disk.Path, err)
	}
	return nil
}

func hasFaulty(array RAIDArray) bool {
	for _, m := range array.Members {
		if m.State == "faulty" {
			return true
		}
	}
	return false
}
