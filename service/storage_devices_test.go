package service

import (
	"encoding/json"
	"testing"

	"github.com/ReCasaOS/CasaOS-LocalStorage/model"
)

func mountedVolume(path, mount string) model.LSBLKModel {
	return model.LSBLKModel{
		Path: path, Name: path, MountPoint: mount, FsType: "ext4", UUID: "uuid-" + path, Label: "storage",
		FSSize: json.Number("1000"), FSAvail: json.Number("800"), FSUsed: json.Number("200"),
	}
}

// raid10 is what lsblk says of a RAID10 on four disks: the array, whole, under
// every member disk, or under every member's partition.
func raid10(mount string, partitionedMembers bool) []model.LSBLKModel {
	array := mountedVolume("/dev/md0", mount)
	array.Type, array.Size = "raid10", 2000

	disks := []model.LSBLKModel{}
	for _, path := range []string{"/dev/sda", "/dev/sdb", "/dev/sdc", "/dev/sdd"} {
		member := model.LSBLKModel{
			Path: path, Name: path, Type: "disk", Tran: "sata", Model: "HDD", Size: 1000,
			FsType: "linux_raid_member", Children: []model.LSBLKModel{array},
		}
		if partitionedMembers {
			part := member
			part.Path, part.Type = path+"1", "part"
			member.FsType, member.Children = "", []model.LSBLKModel{part}
		}
		disks = append(disks, member)
	}

	return disks
}

func TestAnArrayIsOneStorageHoweverManyDisksItSpans(t *testing.T) {
	for _, tc := range []struct {
		name        string
		mount       string
		partitioned bool
		wantName    string
		system      bool
	}{
		{"whole-disk members, data", "/mnt/storage", false, "RAID10", false},
		{"whole-disk members, root", "/", false, "System", true},
		{"partitioned members, data", "/mnt/storage", true, "RAID10", false},
		{"partitioned members, root", "/", true, "System", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			disks := raid10(tc.mount, tc.partitioned)
			before, _ := json.Marshal(disks)

			got := StorageDevices(disks, "")
			if len(got) != 1 || len(got[0].Volumes) != 1 {
				t.Fatalf("the array once, with its filesystem once: %+v", got)
			}
			device := got[0]
			if device.Name != tc.wantName || device.System != tc.system || device.Model != "RAID10" ||
				device.Type != "raid10" || device.Disk.Path != "/dev/md0" || device.Disk.Size != 2000 {
				t.Fatalf("the array is the storage: %+v", device)
			}
			if device.Volumes[0].MountPoint != tc.mount || device.Volumes[0].Children != nil {
				t.Fatalf("the volume: %+v", device.Volumes[0])
			}

			device.Volumes[0].Label = "changed"
			after, _ := json.Marshal(disks)
			if string(before) != string(after) {
				t.Fatal("the lsblk tree is disk management's too, and must come back untouched")
			}
		})
	}
}

func TestADiskLendingItselfToTheSystemArrayIsSystemToo(t *testing.T) {
	disks := raid10("/", false)
	disks[0].Children = append(disks[0].Children, mountedVolume("/dev/sda1", "/boot/efi"))

	got := StorageDevices(disks, "")
	if len(got) != 2 || !got[0].System || !got[1].System {
		t.Fatalf("the root array and the boot partition of one of its members: %+v", got)
	}

	// df names the device of / when lsblk does not show it mounted there
	got = StorageDevices(raid10("/somewhere-else", true), "/dev/md0")
	if len(got) != 1 || !got[0].System || got[0].Name != "System" {
		t.Fatalf("df's device is the system: %+v", got)
	}
}

func TestPartitionsOnAnArrayAreOneStorageWithTwoVolumes(t *testing.T) {
	array := model.LSBLKModel{
		Path: "/dev/md0", Type: "raid1", Size: 2500,
		Children: []model.LSBLKModel{mountedVolume("/dev/md0p1", "/mnt/first"), mountedVolume("/dev/md0p2", "/mnt/second")},
	}
	disks := []model.LSBLKModel{
		{Path: "/dev/sda", Tran: "sata", Children: []model.LSBLKModel{array}},
		{Path: "/dev/sdb", Tran: "sata", Children: []model.LSBLKModel{array}},
	}

	got := StorageDevices(disks, "")
	if len(got) != 1 || len(got[0].Volumes) != 2 || got[0].Disk.Size != 2500 {
		t.Fatalf("one array, two volumes: %+v", got)
	}
	if usage := StorageUsage(disks); usage.Size != 2000 || usage.Avail != 1600 || usage.Used != 400 || usage.MountCount != 2 {
		t.Fatalf("each filesystem counted once: %+v", usage)
	}
}

func TestOrdinaryDisksAreListedAsBefore(t *testing.T) {
	usb := mountedVolume("/dev/sde", "/media/usb")
	usb.Tran = "usb"
	disks := []model.LSBLKModel{
		{Path: "/dev/mmcblk0", Model: "SD32G", Children: []model.LSBLKModel{mountedVolume("/dev/mmcblk0p1", "/boot/firmware"), mountedVolume("/dev/mmcblk0p2", "/")}},
		usb,
		{Path: "/dev/sdf", Model: "WD", Tran: "sata", Children: []model.LSBLKModel{mountedVolume("/dev/sdf1", "/mnt/other")}},
		{Path: "/dev/sdg", FsType: "linux_raid_member"},
		{Path: "/dev/zram0", MountPoint: "[SWAP]", FsType: "swap"},
	}

	got := StorageDevices(disks, "")
	if len(got) != 3 {
		t.Fatalf("three storages, and neither an unassembled member nor swap: %+v", got)
	}
	if !got[0].System || got[0].Name != "System" || got[0].Model != "SD32G" || len(got[0].Volumes) != 2 {
		t.Fatalf("the system card: %+v", got[0])
	}
	if got[1].Type != "usb" || got[1].Volumes[0].Path != "/dev/sde" {
		t.Fatalf("a whole-disk USB filesystem: %+v", got[1])
	}
	if got[2].Name != "WD" || got[2].Model != "WD" || got[2].Type != "sata" {
		t.Fatalf("a data disk: %+v", got[2])
	}
	// the same label on two devices does not make them one
	if got[1].Volumes[0].Path == got[2].Volumes[0].Path {
		t.Fatal("two devices merged")
	}
	if len(StorageDevices(nil, "")) != 0 {
		t.Fatal("nothing from nothing")
	}
}

func TestAFilesystemMountedTwiceIsListedTwiceAndCountedOnce(t *testing.T) {
	disks := raid10("/mnt/storage", true)
	bind := mountedVolume("/dev/md0", "/mnt/bind")
	second := mountedVolume("/dev/md1", "/mnt/second")
	second.Type, second.Size = "raid1", 1100
	disks[0].Children = append(disks[0].Children, bind, second)
	disks[1].Children = append(disks[1].Children, second)

	mounts := 0
	for _, device := range StorageDevices(disks, "") {
		mounts += len(device.Volumes)
	}
	if mounts != 3 {
		t.Fatalf("storage, bind and second, each once: %d", mounts)
	}
	if usage := StorageUsage(disks); usage.Size != 2000 || usage.Avail != 1600 || usage.MountCount != 2 {
		t.Fatalf("md0 once and md1 once: %+v", usage)
	}
}

func TestADiskIsInUseWhenAnythingBelowItIs(t *testing.T) {
	for name, disk := range map[string]model.LSBLKModel{
		"an array member":               {FsType: "linux_raid_member"},
		"a disk whose partition is one": {Children: []model.LSBLKModel{{FsType: "linux_raid_member"}}},
		"an array below a partition":    {Children: []model.LSBLKModel{{Children: []model.LSBLKModel{{Type: "raid1"}}}}},
		"a mount three levels down":     {Children: []model.LSBLKModel{{Children: []model.LSBLKModel{{MountPoint: "/mnt/nested"}}}}},
		"a partition in a volume group": {Children: []model.LSBLKModel{{FsType: "LVM2_member"}}},
		"a ZFS pool member":             {FsType: "zfs_member"},
		"swap on a partition":           {Children: []model.LSBLKModel{{MountPoint: "[SWAP]"}}},
	} {
		if !DiskInUse(disk) {
			t.Errorf("%s: offered for formatting", name)
		}
	}

	for name, disk := range map[string]model.LSBLKModel{
		"an empty disk":               {Path: "/dev/sdx"},
		"an unmounted ext4 partition": {Children: []model.LSBLKModel{{FsType: "ext4"}}},
	} {
		if DiskInUse(disk) {
			t.Errorf("%s: kept from formatting", name)
		}
	}
}
