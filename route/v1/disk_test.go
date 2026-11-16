package v1

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	model1 "github.com/ReCasaOS/CasaOS-LocalStorage/model"
	"github.com/ReCasaOS/CasaOS-LocalStorage/service"
	model2 "github.com/ReCasaOS/CasaOS-LocalStorage/service/model"
	"github.com/labstack/echo/v4"
)

// Only the three DiskService methods GetDiskList touches are implemented; the
// embedded interfaces are nil so any other call panics loudly.
type stubDisk struct {
	service.DiskService
	blk   []model1.LSBLKModel
	smart map[string]model1.SmartctlA
}

func (s stubDisk) LSBLK(bool) []model1.LSBLKModel               { return s.blk }
func (s stubDisk) GetSerialAllFromDB() ([]model2.Volume, error) { return nil, nil }
func (s stubDisk) SmartCTL(path string) model1.SmartctlA        { return s.smart[path] }

type stubServices struct {
	service.Services
	disk service.DiskService
}

func (s stubServices) Disk() service.DiskService { return s.disk }

func TestGetDiskListSmartStatus(t *testing.T) {
	qemu := model1.SmartctlA{ModelName: "QEMU HARDDISK"}
	qemu.Smartctl.ExitStatus = 4
	failed := model1.SmartctlA{ModelName: "ST3000DM001-1CH166", SmartStatus: &model1.SmartStatus{Passed: false}}

	service.MyService = stubServices{disk: stubDisk{
		blk: []model1.LSBLKModel{
			{Name: "sda", Path: "/dev/sda", SubSystems: "block:scsi:virtio:pci", Children: []model1.LSBLKModel{{Name: "sda1", Path: "/dev/sda1", FsType: "ext4", MountPoint: "/"}}},
			{Name: "sdb", Path: "/dev/sdb", Tran: "sata", SubSystems: "block:scsi:pci"},
		},
		smart: map[string]model1.SmartctlA{"/dev/sda": qemu, "/dev/sdb": failed},
	}}
	t.Cleanup(func() { service.MyService = nil })

	e := echo.New()
	rec := httptest.NewRecorder()
	if err := GetDiskList(e.NewContext(httptest.NewRequest(http.MethodGet, "/v1/disks", nil), rec)); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}

	var body struct {
		Data struct {
			Disks []model1.Drive `json:"disks"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	want := []struct{ name, model, health, smart string }{
		{"sda", "System", "true", model1.SmartHealthUnavailable},
		{"sdb", "", "false", model1.SmartHealthFailed},
	}
	if len(body.Data.Disks) != len(want) {
		t.Fatalf("got %d disks, want %d: %s", len(body.Data.Disks), len(want), rec.Body.String())
	}
	for i, w := range want {
		d := body.Data.Disks[i]
		if d.Name != w.name || d.Model != w.model || d.Health != w.health || d.SmartStatus != w.smart {
			t.Errorf("disk %d = {name %q model %q health %q smart_status %q}, want %+v", i, d.Name, d.Model, d.Health, d.SmartStatus, w)
		}
	}
}

// the fixtures name devices (/dev/sda…) that the machine running the tests may have, and hold
func init() { service.DeviceBusy = func(string) bool { return false } }
