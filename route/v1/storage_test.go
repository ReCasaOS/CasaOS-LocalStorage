package v1

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	model1 "github.com/ReCasaOS/CasaOS-LocalStorage/model"
	"github.com/ReCasaOS/CasaOS-LocalStorage/service"
	"github.com/labstack/echo/v4"
)

// storageStubDisk adds what GetStorageList asks for to the stub GetDiskList uses.
type storageStubDisk struct {
	stubDisk
	systemPath string
	dfErr      error
	labels     map[string]string
}

func (s storageStubDisk) GetSystemDf() (model1.DFDiskSpace, error) {
	return model1.DFDiskSpace{FileSystem: s.systemPath}, s.dfErr
}

func (s storageStubDisk) GetPersistentTypeByUUID(string) string { return "fstab" }

func (s storageStubDisk) GetFilesystemLabel(path string) string { return s.labels[path] }

func useStubDisk(t *testing.T, disk service.DiskService) {
	t.Helper()
	service.MyService = stubServices{disk: disk}
	t.Cleanup(func() { service.MyService = nil })
}

func getJSON(t *testing.T, handler echo.HandlerFunc, target string, into interface{}) {
	t.Helper()
	rec := httptest.NewRecorder()
	if err := handler(echo.New().NewContext(httptest.NewRequest(http.MethodGet, target, nil), rec)); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), into); err != nil {
		t.Fatal(err)
	}
}

// raid10Disks is a RAID10 on four SATA disks, as lsblk lists it: the array under
// every member, or under every member's partition.
func raid10Disks(mount string, partitioned bool) []model1.LSBLKModel {
	array := model1.LSBLKModel{
		Path: "/dev/md0", Name: "md0", Type: "raid10", Size: 2000, FsType: "ext4", MountPoint: mount,
		UUID: "raid-volume", Label: "storage", FSSize: "1900", FSAvail: "1800", FSUsed: "100",
	}
	disks := []model1.LSBLKModel{}
	for _, name := range []string{"sda", "sdb", "sdc", "sdd"} {
		member := model1.LSBLKModel{
			Path: "/dev/" + name, Name: name, Type: "disk", Tran: "sata", Size: 1000,
			FsType: "linux_raid_member", Children: []model1.LSBLKModel{array},
		}
		if partitioned {
			part := member
			part.Path, part.Name, part.Type = member.Path+"1", name+"1", "part"
			member.FsType, member.Children = "", []model1.LSBLKModel{part}
		}
		disks = append(disks, member)
	}

	return disks
}

type storageList struct {
	Data []model1.Storages `json:"data"`
}

func TestGetStorageListShowsAnArrayOnce(t *testing.T) {
	for _, partitioned := range []bool{false, true} {
		disks := raid10Disks("/mnt/storage", partitioned)
		useStubDisk(t, storageStubDisk{stubDisk: stubDisk{blk: disks}, dfErr: errors.New("df unavailable")})
		before, _ := json.Marshal(disks)

		var body storageList
		getJSON(t, GetStorageList, "/v1/storage", &body)

		if len(body.Data) != 1 || len(body.Data[0].Children) != 1 {
			t.Fatalf("partitioned=%v: the array once, not once per disk: %+v", partitioned, body.Data)
		}
		storage := body.Data[0]
		if storage.Path != "/dev/md0" || storage.DiskName != "RAID10" || storage.DiskModel != "RAID10" || storage.Size != 2000 || storage.Type != "raid10" {
			t.Fatalf("partitioned=%v: the array is the storage: %+v", partitioned, storage)
		}
		volume := storage.Children[0]
		if volume.Path != "/dev/md0" || volume.MountPoint != "/mnt/storage" || volume.Size != "1900" || volume.Label != "storage" || volume.PersistedIn != "fstab" {
			t.Fatalf("partitioned=%v: the volume keeps what it had: %+v", partitioned, volume)
		}
		after, _ := json.Marshal(disks)
		if string(before) != string(after) {
			t.Fatal("the lsblk tree came back changed")
		}
	}
}

func TestGetStorageListKeepsTheSystemArrayForWhoAsksForIt(t *testing.T) {
	for _, partitioned := range []bool{false, true} {
		for _, dfFails := range []bool{false, true} {
			disk := storageStubDisk{stubDisk: stubDisk{blk: raid10Disks("/", partitioned)}, systemPath: "/dev/md0"}
			if dfFails {
				disk.dfErr = errors.New("df unavailable")
			}
			useStubDisk(t, disk)

			var hidden storageList
			getJSON(t, GetStorageList, "/v1/storage", &hidden)
			if len(hidden.Data) != 0 {
				t.Fatalf("partitioned=%v df fails=%v: the system array listed as user storage: %+v", partitioned, dfFails, hidden.Data)
			}

			var shown storageList
			getJSON(t, GetStorageList, "/v1/storage?system=show", &shown)
			if len(shown.Data) != 1 || shown.Data[0].DiskName != "System" || shown.Data[0].DiskModel != "RAID10" {
				t.Fatalf("partitioned=%v df fails=%v: the system array: %+v", partitioned, dfFails, shown.Data)
			}
		}
	}
}

func TestGetStorageListLabelsAndTheSystemCardAsBefore(t *testing.T) {
	useStubDisk(t, storageStubDisk{
		stubDisk: stubDisk{blk: []model1.LSBLKModel{
			{Path: "/dev/mmcblk0", Model: "SD32G", Children: []model1.LSBLKModel{
				{Path: "/dev/mmcblk0p1", MountPoint: "/boot/firmware", FsType: "vfat"},
				{Path: "/dev/mmcblk0p2", MountPoint: "/", FsType: "ext4"},
				{Path: "/dev/mmcblk0p3", MountPoint: "/boot/efi", FsType: "vfat"},
			}},
			{Path: "/dev/sde", Tran: "usb", MountPoint: "/media/usb", FsType: "exfat", UUID: "usb"},
			{Path: "/dev/sdf", Tran: "sata", Model: "WD", Children: []model1.LSBLKModel{{Path: "/dev/sdf1", MountPoint: "/mnt/sdf1", FsType: "ext4"}}},
		}},
		dfErr:  errors.New("df unavailable"),
		labels: map[string]string{"/dev/sdf1": "Media"},
	})

	var body storageList
	getJSON(t, GetStorageList, "/v1/storage?system=show", &body)
	if len(body.Data) != 3 {
		t.Fatalf("system, usb and sata: %+v", body.Data)
	}
	system, usb, sata := body.Data[0], body.Data[1], body.Data[2]
	if system.DiskName != "System" || system.DiskModel != "SD32G" || len(system.Children) != 2 ||
		system.Children[0].Label != "firmware" || system.Children[1].Label != "System" {
		t.Fatalf("the system card, without /boot/efi: %+v", system)
	}
	if usb.Type != "usb" || len(usb.Children) != 1 || usb.Children[0].Label != "usb" {
		t.Fatalf("a whole-disk USB filesystem, named after its mount point: %+v", usb)
	}
	if sata.DiskName != "WD" || sata.Children[0].Label != "Media" {
		t.Fatalf("the label read from the filesystem when lsblk has none: %+v", sata)
	}
}

func TestGetDiskListNeverOffersAnArrayMemberForFormatting(t *testing.T) {
	for _, partitioned := range []bool{false, true} {
		disks := append(raid10Disks("/mnt/storage", partitioned), model1.LSBLKModel{Name: "sdx", Path: "/dev/sdx", Tran: "sata", Size: 500})
		useStubDisk(t, stubDisk{blk: disks})

		var body struct {
			Data struct {
				Disks []model1.Drive `json:"disks"`
				Avail []model1.Drive `json:"avail"`
			} `json:"data"`
		}
		getJSON(t, GetDiskList, "/v1/disks", &body)

		if len(body.Data.Disks) != 5 {
			t.Fatalf("partitioned=%v: every disk is still a disk: %+v", partitioned, body.Data.Disks)
		}
		if len(body.Data.Avail) != 1 || body.Data.Avail[0].Path != "/dev/sdx" {
			t.Fatalf("partitioned=%v: only the empty disk is offered: %+v", partitioned, body.Data.Avail)
		}
	}
}
