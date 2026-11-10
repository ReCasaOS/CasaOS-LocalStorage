package service

import (
	"errors"
	"strconv"
	"strings"

	"github.com/ReCasaOS/CasaOS-LocalStorage/model"
)

// Storage as a person sees it.
//
// lsblk describes block devices, and a RAID array is one device with several
// parents: it is listed again under every member disk. Walking each disk for its
// mounted filesystems therefore found an array once per member, so a RAID10 of
// four disks was four storages holding the same filesystem, and its space was
// counted four times wherever it was added up. This view is built from the same
// tree without changing it, since disk management still needs every member: an
// array is a storage of its own, each mounted filesystem is listed once, and a
// disk that only lends itself to an array does not appear.
//
// Ported from IceWhaleTech/CasaOS-LocalStorage#72 onto this distribution's
// storage accounting.

// StorageDevice is one storage: a disk, or an array however many disks it
// spans, with the filesystems mounted on it.
type StorageDevice struct {
	// Disk is the disk or the array, without its children or mount point.
	Disk model.LSBLKModel
	// Name is what the storage is called: System, the array's level, or the
	// disk's model.
	Name string
	// Model is the disk's model, or the array's level.
	Model string
	// Type is the disk's transport, or the array's type as lsblk writes it.
	Type string
	// System says the storage holds the root filesystem, or lends itself to
	// an array that does.
	System bool
	// Volumes are the mounted filesystems, each device and mount point once,
	// without their children.
	Volumes []model.LSBLKModel
}

// StorageDevices is the storage list of an lsblk tree. systemPath is the
// device df reports for /, or empty when df could not say.
func StorageDevices(disks []model.LSBLKModel, systemPath string) []StorageDevice {
	view := storageView{systemPath: systemPath, groups: map[string]int{}, seen: map[[2]string]bool{}}
	for i, disk := range disks {
		key := disk.Path
		if key == "" {
			key = "#" + strconv.Itoa(i)
		}
		view.visit(disk, view.owner(disk, false), key)
	}

	return view.devices
}

type storageView struct {
	systemPath string
	devices    []StorageDevice
	groups     map[string]int
	seen       map[[2]string]bool
}

func (v *storageView) owner(node model.LSBLKModel, array bool) StorageDevice {
	disk := node
	disk.Children = nil
	disk.MountPoint = ""

	device := StorageDevice{Disk: disk, Name: node.Model, Model: node.Model, Type: node.Tran}
	if array {
		level := strings.ToUpper(node.Type)
		device.Name, device.Model, device.Type = level, level, node.Type
	}
	if containsSystemStorage(node, v.systemPath) {
		device.Name, device.System = "System", true
	}

	return device
}

func (v *storageView) visit(node model.LSBLKModel, owner StorageDevice, key string) {
	if strings.HasPrefix(node.Type, "raid") && node.Path != "" {
		owner, key = v.owner(node, true), node.Path
	}
	v.add(node, owner, key)
	for _, child := range node.Children {
		v.visit(child, owner, key)
	}
}

func (v *storageView) add(node model.LSBLKModel, owner StorageDevice, key string) {
	if node.MountPoint == "" || node.MountPoint == "[SWAP]" {
		return
	}
	if node.Path != "" {
		mount := [2]string{node.Path, node.MountPoint}
		if v.seen[mount] {
			return
		}
		v.seen[mount] = true
	}

	index, ok := v.groups[key]
	if !ok {
		index = len(v.devices)
		v.groups[key] = index
		v.devices = append(v.devices, owner)
	}

	volume := node
	volume.Children = nil
	v.devices[index].Volumes = append(v.devices[index].Volumes, volume)
}

func containsSystemStorage(node model.LSBLKModel, systemPath string) bool {
	if node.MountPoint == "/" || (systemPath != "" && node.Path == systemPath) {
		return true
	}
	for _, child := range node.Children {
		if containsSystemStorage(child, systemPath) {
			return true
		}
	}

	return false
}

// isMember says whether what a device holds is a part of something else: a RAID array, a
// volume group, a ZFS pool, or an encrypted volume.
func isMember(fsType string) bool {
	switch fsType {
	case "linux_raid_member", "LVM2_member", "zfs_member", "crypto_LUKS", "BitLocker":
		return true
	}
	return false
}

var (
	// ErrNotABlockDevice: the path is not that of a block device lsblk knows under /dev/.
	ErrNotABlockDevice = errors.New("not a block device")
	// ErrDeviceInUse: erasing the device would take something away that is in use.
	ErrDeviceInUse = errors.New("the device is in use: it holds the system, a mounted filesystem, or a part of an array, a volume group, a pool or an encrypted volume")
)

// EraseRefusal says why the device at path, as lsblk describes it, must not be erased; nil when
// it may. The dashboard offers only what is free, but its list may be old and the routes take
// any path: this is the box's own look at the device, just before it erases it.
//
// A new storage is made of a whole disk that holds nothing in use (DiskInUse). With own, the
// device is one storage about to be formatted again: it is mounted, where this box mounts a
// storage, and may be an array, which is what a storage is; but never something mounted
// elsewhere, the swap, a part of something else, or the place of anything in use below it.
func EraseRefusal(path string, blk model.LSBLKModel, own bool) error {
	if !strings.HasPrefix(path, "/dev/") || blk.Path == "" {
		return ErrNotABlockDevice
	}
	if !own {
		if blk.Type != "disk" { // a partition or a logical volume is not given a partition table
			return ErrNotABlockDevice
		}
		if DiskInUse(blk) {
			return ErrDeviceInUse
		}
		return nil
	}
	if mountedElsewhere(blk) || blk.FsType == "swap" || isMember(blk.FsType) {
		return ErrDeviceInUse
	}
	for _, child := range blk.Children {
		if DiskInUse(child) {
			return ErrDeviceInUse
		}
	}
	return nil
}

// UnmountRefusal says why the storages of the device at path must not be unmounted and their
// folders removed; nil when they may. A storage is mounted where this box mounts one: anything
// mounted elsewhere, on the device or below it, is the system's or the owner's own.
func UnmountRefusal(path string, blk model.LSBLKModel) error {
	if !strings.HasPrefix(path, "/dev/") || blk.Path == "" {
		return ErrNotABlockDevice
	}
	if mountedElsewhere(blk) {
		return ErrDeviceInUse
	}
	return nil
}

// mountedElsewhere says whether the device, or anything below it, is mounted outside of where
// this box mounts a storage: /media (its own mounts, and the USB ones) and /mnt (older
// installs). The system, its boot partition, the swap and a /home of its own all are.
func mountedElsewhere(blk model.LSBLKModel) bool {
	if blk.MountPoint != "" && !strings.HasPrefix(blk.MountPoint, "/media/") && !strings.HasPrefix(blk.MountPoint, "/mnt/") {
		return true
	}
	for _, child := range blk.Children {
		if mountedElsewhere(child) {
			return true
		}
	}
	return false
}

// StorageUsage adds up the space of the mounted filesystems of an lsblk tree,
// each filesystem once however many disks and mount points it appears under.
func StorageUsage(disks []model.LSBLKModel) FilesystemStats {
	stats := FilesystemStats{}
	counted := map[string]bool{}
	for _, device := range StorageDevices(disks, "") {
		for _, volume := range device.Volumes {
			if volume.Path != "" {
				if counted[volume.Path] {
					continue
				}
				counted[volume.Path] = true
			}
			mounted, ok := parseFilesystemStats(volume)
			if !ok {
				continue
			}
			stats.Size += mounted.Size
			stats.Avail += mounted.Avail
			stats.Used += mounted.Used
			stats.MountCount++
		}
	}

	return stats
}

// DiskInUse says whether a disk holds anything that must not be offered for
// formatting: a filesystem or swap mounted anywhere below it, a part of it that
// belongs to a RAID array, a volume group or a ZFS pool, whether or not that
// array, group or pool is assembled right now, or an encrypted volume (LUKS,
// BitLocker), open or not. A locked container mounts nothing and its format is
// not one CasaOS reads, so formatting was the only thing offered for it.
func DiskInUse(disk model.LSBLKModel) bool {
	if disk.MountPoint != "" || strings.HasPrefix(disk.Type, "raid") || isMember(disk.FsType) {
		return true
	}
	for _, child := range disk.Children {
		if DiskInUse(child) {
			return true
		}
	}

	return false
}
