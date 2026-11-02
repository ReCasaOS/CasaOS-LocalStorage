package v1

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/ReCasaOS/CasaOS-Common/utils/logger"
	"github.com/ReCasaOS/CasaOS-LocalStorage/pkg/config"
	"github.com/labstack/echo/v4"
)

// snapraidCall calls a handler as the router would and answers the status and the body.
func snapraidCall(t *testing.T, handler echo.HandlerFunc, method, body string, action string) (int, map[string]interface{}) {
	t.Helper()
	logger.LogInitConsoleOnly()
	saved := config.AppInfo.DBPath
	config.AppInfo.DBPath = t.TempDir()
	t.Cleanup(func() { config.AppInfo.DBPath = saved })

	request := httptest.NewRequest(method, "/v1/disks/snapraid", strings.NewReader(body))
	if body != "" {
		request.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	}
	rec := httptest.NewRecorder()
	c := echo.New().NewContext(request, rec)
	if action != "" {
		c.SetParamNames("action")
		c.SetParamValues(action)
	}
	if err := handler(c); err != nil {
		t.Fatal(err)
	}
	var answer map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &answer); err != nil {
		t.Fatalf("%s: %v", rec.Body.String(), err)
	}
	return rec.Code, answer
}

func TestNothingIsSetUpAtFirst(t *testing.T) {
	code, answer := snapraidCall(t, GetSnapRAID, http.MethodGet, "", "")
	data, _ := answer["data"].(map[string]interface{})
	if code != http.StatusOK || data["configured"] != false {
		t.Fatalf("%d %v", code, answer)
	}
}

func TestAnActionThatIsNotOneIsABadRequestAndOneWithoutASetupIsNotFound(t *testing.T) {
	if code, _ := snapraidCall(t, PostSnapRAIDAction, http.MethodPost, "", "format"); code != http.StatusBadRequest {
		t.Errorf("unknown action: %d", code)
	}
	// no body at all
	if code, _ := snapraidCall(t, PostSnapRAIDAction, http.MethodPost, "", "sync"); code != http.StatusNotFound {
		t.Errorf("sync without a setup: %d", code)
	}
	if code, _ := snapraidCall(t, PostSnapRAIDAction, http.MethodPost, `{"force": true}`, "SYNC"); code != http.StatusNotFound {
		t.Errorf("SYNC with a body: %d", code)
	}
	if code, _ := snapraidCall(t, DeleteSnapRAIDJob, http.MethodDelete, "", ""); code != http.StatusNotFound {
		t.Errorf("stopping nothing: %d", code)
	}
}

func TestAWrongSetupIsABadRequest(t *testing.T) {
	for name, body := range map[string]string{
		"three parity disks": `{"data": ["/a"], "parity": ["/b", "/c", "/d"]}`,
		"no parity disk":     `{"data": ["/a"]}`,
		"no data disk":       `{"parity": ["/b"]}`,
		"not json":           `{"data": `,
	} {
		if code, _ := snapraidCall(t, PutSnapRAID, http.MethodPut, body, ""); code != http.StatusBadRequest {
			t.Errorf("%s: %d", name, code)
		}
	}
}

func TestADiskThatIsNotAMountPointIsAConflict(t *testing.T) {
	if _, err := os.Stat("/proc/self/mountinfo"); err != nil {
		t.Skip("needs /proc")
	}
	code, answer := snapraidCall(t, PutSnapRAID, http.MethodPut, `{"data": ["/nowhere/d1"], "parity": ["/nowhere/p1"]}`, "")
	if code != http.StatusConflict || !strings.Contains(answer["message"].(string), "not mounted") {
		t.Fatalf("%d %v", code, answer)
	}
}

func TestAPoolStandsForItsDisks(t *testing.T) {
	saved := snapraidPoolDisks
	t.Cleanup(func() { snapraidPoolDisks = saved })

	var asked string
	snapraidPoolDisks = func(pool string) ([]string, error) {
		asked = pool
		return nil, ErrSnapRAIDNoPool
	}
	if code, _ := snapraidCall(t, PutSnapRAID, http.MethodPut, `{"pool": "/media/pool", "parity": ["/media/p"]}`, ""); code != http.StatusNotFound || asked != "/media/pool" {
		t.Errorf("an unknown pool: %d (asked %q)", code, asked)
	}

	snapraidPoolDisks = func(string) ([]string, error) { return nil, errors.New("database is locked") }
	if code, _ := snapraidCall(t, PutSnapRAID, http.MethodPut, `{"pool": "/media/pool", "parity": ["/media/p"]}`, ""); code != http.StatusInternalServerError {
		t.Errorf("a database error: %d", code)
	}

	// its disks are taken as the data disks: with three parity disks the setup is refused as a bad request, not as a pool without disks
	snapraidPoolDisks = func(string) ([]string, error) { return []string{"/media/a", "/media/b"}, nil }
	if code, _ := snapraidCall(t, PutSnapRAID, http.MethodPut, `{"pool": "/media/pool", "parity": ["/1", "/2", "/3"]}`, ""); code != http.StatusBadRequest {
		t.Errorf("a pool and too many parity disks: %d", code)
	}
}
