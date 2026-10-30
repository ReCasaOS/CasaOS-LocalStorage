package v1

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ReCasaOS/CasaOS-Common/utils/common_err"
	"github.com/ReCasaOS/CasaOS-Common/utils/logger"
	model1 "github.com/ReCasaOS/CasaOS-LocalStorage/model"
	"github.com/labstack/echo/v4"
)

// raidStubDisk answers what PostRAIDArray asks before it reaches mdadm: the disks, and a cache to drop.
type raidStubDisk struct{ stubDisk }

func (raidStubDisk) RemoveLSBLKCache() {}

func postRAIDArray(t *testing.T, body string) (int, map[string]interface{}) {
	t.Helper()
	logger.LogInitConsoleOnly()
	request := httptest.NewRequest(http.MethodPost, "/v1/disks/raid", strings.NewReader(body))
	request.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	if err := PostRAIDArray(echo.New().NewContext(request, rec)); err != nil {
		t.Fatalf("%s: %v", body, err)
	}
	var answer map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &answer); err != nil {
		t.Fatalf("%s: %v", rec.Body.String(), err)
	}
	return rec.Code, answer
}

// The services are not set up here: a request that went past the name and its confirmation would panic.
func TestAnArrayRequestWithoutItsNameTypedAgainIsABadRequest(t *testing.T) {
	for _, body := range []string{
		`{"name": "pool", "level": "raid1", "disks": ["/dev/sdb", "/dev/sdc"]}`,
		`{"name": "pool", "level": "raid1", "disks": ["/dev/sdb", "/dev/sdc"], "confirm": "other"}`,
		`{"name": "", "level": "raid1", "disks": ["/dev/sdb", "/dev/sdc"], "confirm": ""}`,
		`{"name": 42, "level": "raid1", "disks": ["/dev/sdb", "/dev/sdc"], "confirm": 42}`,
	} {
		if code, _ := postRAIDArray(t, body); code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", body, code)
		}
	}
}

func TestAnArrayIsNotGivenANameTheStorageListCannotCarry(t *testing.T) {
	for _, name := range []string{"a b", "pool;rm", "../pool", "école", "seventeen_letters"} {
		body := `{"name": "` + name + `", "level": "raid1", "disks": ["/dev/sdb", "/dev/sdc"], "confirm": "` + name + `"}`
		if code, _ := postRAIDArray(t, body); code != http.StatusBadRequest {
			t.Errorf("%q: status %d, want 400", name, code)
		}
	}
}

// Whatever is refused is refused before a disk is wiped: the stub has no command to run, and the guard on each disk is let go again.
func TestAnArrayThatCannotBeMadeLeavesTheDisksAlone(t *testing.T) {
	free := func(name string) model1.LSBLKModel {
		return model1.LSBLKModel{Name: name, Path: "/dev/" + name, Type: "disk", Tran: "sata", Size: 2000}
	}
	system := model1.LSBLKModel{
		Name: "sda", Path: "/dev/sda", Type: "disk", Tran: "sata", Size: 2000,
		Children: []model1.LSBLKModel{{Name: "sda1", Path: "/dev/sda1", Type: "part", FsType: "ext4", MountPoint: "/"}},
	}
	used := free("sde")
	used.FsType, used.MountPoint = "ext4", "/media/data"
	useStubDisk(t, raidStubDisk{stubDisk{blk: []model1.LSBLKModel{system, free("sdb"), free("sdc"), free("sdd"), used}}})

	for name, c := range map[string]struct{ level, disks string }{
		"the system disk":            {"raid1", `["/dev/sdb", "/dev/sda"]`},
		"a disk that holds a volume": {"raid1", `["/dev/sdb", "/dev/sde"]`},
		"a disk this box lacks":      {"raid1", `["/dev/sdb", "/dev/nope"]`},
		"raid0 without consent":      {"raid0", `["/dev/sdb", "/dev/sdc"]`},
		"linear":                     {"linear", `["/dev/sdb", "/dev/sdc"]`},
		"no level":                   {"", `["/dev/sdb", "/dev/sdc"]`},
		"raid5 on two disks":         {"raid5", `["/dev/sdb", "/dev/sdc"]`},
		"raid10 on three disks":      {"raid10", `["/dev/sdb", "/dev/sdc", "/dev/sdd"]`},
		"one disk named twice":       {"raid1", `["/dev/sdb", "/dev/sdb"]`},
		"no disk":                    {"raid1", `[]`},
	} {
		body := `{"name": "pool", "level": "` + c.level + `", "disks": ` + c.disks + `, "confirm": "pool"}`
		if code, _ := postRAIDArray(t, body); code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", name, code)
		}
		if len(diskMap) != 0 {
			t.Errorf("%s: disks still guarded: %v", name, diskMap)
		}
	}
}

// A RAID 0 that is agreed to is still held to the rest of the checks.
func TestARaid0ThatIsAgreedToStillNeedsItsDisks(t *testing.T) {
	useStubDisk(t, raidStubDisk{})
	body := `{"name": "fast", "level": "raid0", "disks": ["/dev/sdb"], "confirm": "fast", "accept_data_loss": true}`
	if code, _ := postRAIDArray(t, body); code != http.StatusBadRequest {
		t.Errorf("status %d, want 400", code)
	}
	if len(diskMap) != 0 {
		t.Errorf("disks still guarded: %v", diskMap)
	}
}

func TestAnArrayWaitsForADiskAnotherCallIsWorkingOn(t *testing.T) {
	useStubDisk(t, raidStubDisk{})
	diskMap["/dev/sdc"] = "busying"
	t.Cleanup(func() { delete(diskMap, "/dev/sdc") })

	_, answer := postRAIDArray(t, `{"name": "pool", "level": "raid1", "disks": ["/dev/sdb", "/dev/sdc"], "confirm": "pool"}`)
	if answer["success"] != float64(common_err.DISK_BUSYING) {
		t.Errorf("answer %v, want DISK_BUSYING", answer)
	}
	if _, ok := diskMap["/dev/sdb"]; ok {
		t.Errorf("the free disk was guarded for a call that did not go on: %v", diskMap)
	}
	if _, ok := diskMap["/dev/sdc"]; !ok {
		t.Errorf("the other call's guard was let go: %v", diskMap)
	}
}
