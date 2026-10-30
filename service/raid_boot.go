package service

import (
	"strings"
	"time"

	"github.com/ReCasaOS/CasaOS-Common/utils/logger"
	"github.com/ReCasaOS/CasaOS-LocalStorage/model"
)

// An array is assembled by udev as its disks show up, and the unit that mounts the storages again at boot (casaos-local-storage-first, before docker) does not wait
// for udev: a storage on an array that comes up a second late would stay unmounted, and the apps would write to the folder under it, on the system disk.

// raidAssemblyTries is how many seconds the boot waits, at most, for the arrays of the disks it sees. A disk left over from an array that is gone costs them all.
const raidAssemblyTries = 15

// raidMemberWithoutArray is true when a disk or partition of disks carries a RAID header and has no array on it (yet).
func raidMemberWithoutArray(disks []model.LSBLKModel) bool {
	for _, d := range disks {
		if d.FsType == "linux_raid_member" && !hasArrayBelow(d) || raidMemberWithoutArray(d.Children) {
			return true
		}
	}
	return false
}

func hasArrayBelow(d model.LSBLKModel) bool {
	for _, c := range d.Children {
		if strings.HasPrefix(c.Type, "raid") || hasArrayBelow(c) {
			return true
		}
	}
	return false
}

// awaitRAIDArrays reads the disks with list again, once a second (wait), while one carries a RAID header with no array on it, for tries seconds at most.
func awaitRAIDArrays(disks []model.LSBLKModel, list func() []model.LSBLKModel, wait func(), tries int) []model.LSBLKModel {
	for ; tries > 0 && raidMemberWithoutArray(disks); tries-- {
		logger.Info("a disk carries a RAID header and its array is not there yet: waiting for it")
		wait()
		disks = list()
	}
	return disks
}

func (d *diskService) awaitRAIDArrays(disks []model.LSBLKModel) []model.LSBLKModel {
	return awaitRAIDArrays(disks, func() []model.LSBLKModel { return d.LSBLK(false) }, func() { time.Sleep(time.Second) }, raidAssemblyTries)
}
