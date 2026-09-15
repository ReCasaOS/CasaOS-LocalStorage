package service

import (
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
// formatting: a filesystem or swap mounted anywhere below it, or a part of it
// that belongs to a RAID array, a volume group or a ZFS pool, whether or not
// that array, group or pool is assembled right now.
func DiskInUse(disk model.LSBLKModel) bool {
	switch {
	case disk.MountPoint != "",
		strings.HasPrefix(disk.Type, "raid"),
		disk.FsType == "linux_raid_member",
		disk.FsType == "LVM2_member",
		disk.FsType == "zfs_member":
		return true
	}
	for _, child := range disk.Children {
		if DiskInUse(child) {
			return true
		}
	}

	return false
}
