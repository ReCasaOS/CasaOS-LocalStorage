package v1

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ReCasaOS/CasaOS-Common/utils/logger"
	"github.com/labstack/echo/v4"
)

func postRAIDLevel(t *testing.T, name, body string) (int, map[string]interface{}) {
	t.Helper()
	logger.LogInitConsoleOnly()
	request := httptest.NewRequest(http.MethodPost, "/v1/disks/raid/"+name+"/level", strings.NewReader(body))
	request.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	ctx := echo.New().NewContext(request, rec)
	ctx.SetParamNames("name")
	ctx.SetParamValues(name)
	if err := PostRAIDLevel(ctx); err != nil {
		t.Fatalf("%s: %v", body, err)
	}
	var answer map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &answer); err != nil {
		t.Fatalf("%s: %v", rec.Body.String(), err)
	}
	return rec.Code, answer
}

// Whatever is refused is refused before the services are asked anything, or before a disk is touched: the services are not set up here.
func TestAChangeOfLevelNeedsItsDiskTypedAgainAndRAID5(t *testing.T) {
	for name, body := range map[string]string{
		"no confirmation":    `{"level": "raid5", "path": "/dev/sdd"}`,
		"another disk typed": `{"level": "raid5", "path": "/dev/sdd", "confirm": "sde"}`,
		"no disk":            `{"level": "raid5", "confirm": ""}`,
		"RAID 6":             `{"level": "raid6", "path": "/dev/sdd", "confirm": "sdd"}`,
		"RAID 0":             `{"level": "raid0", "path": "/dev/sdd", "confirm": "sdd"}`,
		"no level":           `{"path": "/dev/sdd", "confirm": "sdd"}`,
	} {
		if code, _ := postRAIDLevel(t, "md0", body); code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", name, code)
		}
		if len(diskMap) != 0 {
			t.Errorf("%s: disks still guarded: %v", name, diskMap)
		}
	}
}

func TestAChangeOfLevelWaitsForADiskAnotherCallIsWorkingOn(t *testing.T) {
	diskMap["/dev/sdd"] = "busying"
	t.Cleanup(func() { delete(diskMap, "/dev/sdd") })

	_, answer := postRAIDLevel(t, "md0", `{"level": "raid5", "path": "/dev/sdd", "confirm": "sdd"}`)
	if answer["success"] == nil || answer["message"] == "" {
		t.Errorf("answer %v", answer)
	}
	if _, ok := diskMap["/dev/sdd"]; !ok {
		t.Errorf("the other call's guard was let go: %v", diskMap)
	}
}
