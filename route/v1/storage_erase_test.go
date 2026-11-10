package v1

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ReCasaOS/CasaOS-Common/utils/logger"
	model1 "github.com/ReCasaOS/CasaOS-LocalStorage/model"
	"github.com/ReCasaOS/CasaOS-LocalStorage/pkg/config"
	"github.com/labstack/echo/v4"
)

// eraseStubDisk answers lsblk from a map and records what a route asks to be done to a
// disk. Each step fails, so that a route that goes on stops at its first one.
type eraseStubDisk struct {
	stubDisk
	info map[string]model1.LSBLKModel
	did  *[]string
}

var errStub = errors.New("stopped by the stub")

func (s eraseStubDisk) GetDiskInfo(path string) model1.LSBLKModel { return s.info[path] }
func (s eraseStubDisk) RemoveLSBLKCache()                         {}
func (s eraseStubDisk) UmountPointAndRemoveDir(m model1.LSBLKModel) error {
	*s.did = append(*s.did, "unmount "+m.Path)
	return errStub
}

func (s eraseStubDisk) DeletePartition(path string) error {
	*s.did = append(*s.did, "delete the partitions of "+path)
	return errStub
}

func (s eraseStubDisk) FormatDisk(path string) error {
	*s.did = append(*s.did, "format "+path)
	return errStub
}

func (s eraseStubDisk) MountDisk(path, _ string) (string, error) {
	*s.did = append(*s.did, "mount "+path)
	return "", errStub
}

func part(path, fsType, mountPoint string) model1.LSBLKModel {
	return model1.LSBLKModel{Path: path, Name: strings.TrimPrefix(path, "/dev/"), Type: "part", FsType: fsType, MountPoint: mountPoint}
}

func wholeDisk(path, fsType string, children ...model1.LSBLKModel) model1.LSBLKModel {
	return model1.LSBLKModel{Path: path, Name: strings.TrimPrefix(path, "/dev/"), Type: "disk", FsType: fsType, Children: children}
}

// the disks of a box
var eraseBox = map[string]model1.LSBLKModel{
	// the system's
	"/dev/sda":  wholeDisk("/dev/sda", "", part("/dev/sda1", "vfat", "/boot/efi"), part("/dev/sda2", "ext4", "/"), part("/dev/sda3", "swap", "[SWAP]")),
	"/dev/sda1": part("/dev/sda1", "vfat", "/boot/efi"),
	"/dev/sda2": part("/dev/sda2", "ext4", "/"),
	"/dev/sda3": part("/dev/sda3", "swap", "[SWAP]"),
	// a member of an array, a volume group, an encrypted volume that is open
	"/dev/sdb":  wholeDisk("/dev/sdb", "linux_raid_member"),
	"/dev/sdc":  wholeDisk("/dev/sdc", "", part("/dev/sdc1", "LVM2_member", "")),
	"/dev/sdc1": part("/dev/sdc1", "LVM2_member", ""),
	"/dev/sdd1": {Path: "/dev/sdd1", Name: "sdd1", Type: "part", FsType: "crypto_LUKS", Children: []model1.LSBLKModel{
		{Path: "/dev/mapper/vault", Name: "vault", Type: "crypt", FsType: "ext4", MountPoint: "/media/vault"},
	}},
	// a storage, on its disk
	"/dev/sde":  wholeDisk("/dev/sde", "", part("/dev/sde1", "ext4", "/media/Storage1")),
	"/dev/sde1": part("/dev/sde1", "ext4", "/media/Storage1"),
	// a blank disk
	"/dev/sdf": wholeDisk("/dev/sdf", ""),
	// an array and a logical volume that are storages, and a storage of an older install
	"/dev/md0":            {Path: "/dev/md0", Name: "md0", Type: "raid1", FsType: "ext4", MountPoint: "/media/Array"},
	"/dev/mapper/vg-data": {Path: "/dev/mapper/vg-data", Name: "vg-data", Type: "lvm", FsType: "ext4", MountPoint: "/media/Data"},
	"/dev/sdh1":           part("/dev/sdh1", "ext4", "/mnt/Old"),
	// a /home of its own, which the owner mounted and the box did not
	"/dev/sdg":  wholeDisk("/dev/sdg", "", part("/dev/sdg1", "ext4", "/home")),
	"/dev/sdg1": part("/dev/sdg1", "ext4", "/home"),
	// a logical volume with nothing on it
	"/dev/mapper/vg-free": {Path: "/dev/mapper/vg-free", Name: "vg-free", Type: "lvm"},
}

// what a route answered, and what it had done to the disks by then; merged are the mount
// points the merged storage is made of
func erase(t *testing.T, handler echo.HandlerFunc, method, body string, merged ...string) (int, string, []string) {
	t.Helper()
	logger.LogInitConsoleOnly()
	saved, taken := config.AppInfo.DBPath, raidTakenMounts
	config.AppInfo.DBPath = t.TempDir() // no SnapRAID here
	raidTakenMounts = func() []string { return merged }
	t.Cleanup(func() { config.AppInfo.DBPath, raidTakenMounts = saved, taken })
	did := []string{}
	useStubDisk(t, eraseStubDisk{info: eraseBox, did: &did})

	request := httptest.NewRequest(method, "/v1/storage", strings.NewReader(body))
	request.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	if err := handler(echo.New().NewContext(request, rec)); err != nil {
		t.Fatal(err)
	}
	return rec.Code, rec.Body.String(), did
}

type refusal struct {
	path string
	code int
}

// The dashboard offers only the disks that are free, but its list may be old and the
// route takes any path: the box itself looks at the disk before it erases it.
func TestAddingAStorageDoesNotEraseADiskThatIsNotFree(t *testing.T) {
	for what, c := range map[string]refusal{
		"a name that is not a device":      {"sdf", http.StatusBadRequest},
		"a file":                           {"/etc/passwd", http.StatusBadRequest},
		"a device that is not there":       {"/dev/sdz", http.StatusBadRequest},
		"a device that is not a disk":      {"/dev/mapper/vg-free", http.StatusBadRequest},
		"the disk of the system":           {"/dev/sda", http.StatusConflict},
		"a member of an array":             {"/dev/sdb", http.StatusConflict},
		"a disk that holds a volume group": {"/dev/sdc", http.StatusConflict},
		"a disk with a mounted filesystem": {"/dev/sde", http.StatusConflict},
	} {
		code, body, did := erase(t, PostAddStorage, http.MethodPost, `{"path":"`+c.path+`","name":"Storage","format":true}`)
		if code != c.code || len(did) != 0 {
			t.Errorf("%s: %d %s, after %v; want %d and nothing done", what, code, body, did, c.code)
		}
	}

	code, body, did := erase(t, PostAddStorage, http.MethodPost, `{"path":"/dev/sdf","name":"Storage","format":true}`)
	if len(did) == 0 || did[0] != "unmount /dev/sdf" {
		t.Errorf("a blank disk: %d %s, after %v; want it erased", code, body, did)
	}

	// without the format nothing is erased: a disk that holds a filesystem is the one to mount
	code, body, did = erase(t, PostAddStorage, http.MethodPost, `{"path":"/dev/sde","name":"Storage","format":false}`)
	if code == http.StatusConflict || len(did) == 0 || did[0] != "mount /dev/sde1" {
		t.Errorf("mounting what a disk holds: %d %s, after %v; want its filesystem mounted", code, body, did)
	}
	if code, body, did := erase(t, PostAddStorage, http.MethodPost, `{"path":"sde","name":"Storage","format":false}`); code != http.StatusBadRequest || len(did) != 0 {
		t.Errorf("mounting a name that is not a device: %d %s, after %v; want 400", code, body, did)
	}
}

func TestFormattingAStorageDoesNotEraseWhatIsNotOne(t *testing.T) {
	for what, c := range map[string]refusal{
		"a name that is not a device":         {"sde1", http.StatusBadRequest},
		"a device that is not there":          {"/dev/sdz1", http.StatusBadRequest},
		"the filesystem of the system":        {"/dev/sda2", http.StatusConflict},
		"the partition the box starts from":   {"/dev/sda1", http.StatusConflict},
		"the swap":                            {"/dev/sda3", http.StatusConflict},
		"the whole disk of the system":        {"/dev/sda", http.StatusConflict},
		"a member of an array":                {"/dev/sdb", http.StatusConflict},
		"a part of a volume group":            {"/dev/sdc1", http.StatusConflict},
		"an encrypted volume that is open":    {"/dev/sdd1", http.StatusConflict},
		"a disk whose partition is a storage": {"/dev/sde", http.StatusConflict},
		"a /home of its own":                  {"/dev/sdg1", http.StatusConflict},
	} {
		code, body, did := erase(t, PutFormatStorage, http.MethodPut, `{"path":"`+c.path+`","volume":""}`)
		if code != c.code || len(did) != 0 {
			t.Errorf("%s: %d %s, after %v; want %d and nothing done", what, code, body, did, c.code)
		}
	}

	// a storage is mounted, and that is the one case its own mount point does not refuse;
	// it may be a partition, an array or a logical volume, under /media or under /mnt
	for _, path := range []string{"/dev/sde1", "/dev/md0", "/dev/mapper/vg-data", "/dev/sdh1"} {
		code, body, did := erase(t, PutFormatStorage, http.MethodPut, `{"path":"`+path+`","volume":""}`)
		if len(did) == 0 || did[0] != "unmount "+path {
			t.Errorf("the storage %s: %d %s, after %v; want it unmounted to be formatted", path, code, body, did)
		}
	}

	// unless the merged storage is made of it
	code, body, did := erase(t, PutFormatStorage, http.MethodPut, `{"path":"/dev/sde1","volume":""}`, "/media/Storage1")
	if code != http.StatusConflict || len(did) != 0 {
		t.Errorf("a storage the merged storage is made of: %d %s, after %v; want 409 and nothing done", code, body, did)
	}
}

// Removing a storage unmounts it and deletes the folder it was mounted on. Asked of the
// system's disk, it took /boot/efi and its folder away before it stopped at /.
func TestRemovingAStorageDoesNotUnmountWhatIsNotOne(t *testing.T) {
	for what, c := range map[string]struct {
		handler echo.HandlerFunc
		body    string
		code    int
	}{
		"the disk of the system":                {DeleteDisksUmount, `{"path":"/dev/sda"}`, http.StatusConflict},
		"a disk with a /home of its own":        {DeleteDisksUmount, `{"path":"/dev/sdg"}`, http.StatusConflict},
		"a name that is not a device":           {DeleteDisksUmount, `{"path":"sde"}`, http.StatusBadRequest},
		"the filesystem of the system":          {DeleteStorage, `{"path":"/dev/sda2","volume":"/"}`, http.StatusConflict},
		"the partition the box starts from":     {DeleteStorage, `{"path":"/dev/sda1","volume":"/boot/efi"}`, http.StatusConflict},
		"a device that is not there, as a path": {DeleteStorage, `{"path":"/dev/sdz1","volume":"/media/x"}`, http.StatusBadRequest},
	} {
		code, body, did := erase(t, c.handler, http.MethodDelete, c.body)
		if code != c.code || len(did) != 0 {
			t.Errorf("%s: %d %s, after %v; want %d and nothing done", what, code, body, did, c.code)
		}
	}

	if code, body, did := erase(t, DeleteDisksUmount, http.MethodDelete, `{"path":"/dev/sde"}`); len(did) == 0 || did[0] != "unmount /dev/sde1" {
		t.Errorf("a disk whose partition is a storage: %d %s, after %v; want it unmounted", code, body, did)
	}
	if code, body, did := erase(t, DeleteStorage, http.MethodDelete, `{"path":"/dev/sde1","volume":"/media/Storage1"}`); len(did) == 0 || did[0] != "unmount /dev/sde1" {
		t.Errorf("a storage: %d %s, after %v; want it unmounted", code, body, did)
	}
}
