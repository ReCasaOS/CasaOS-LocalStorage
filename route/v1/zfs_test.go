package v1

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/labstack/echo/v4"
)

func zfsCall(t *testing.T, handler echo.HandlerFunc, target string) (int, map[string]interface{}) {
	t.Helper()
	rec := httptest.NewRecorder()
	c := echo.New().NewContext(httptest.NewRequest(http.MethodGet, target, nil), rec)
	if err := handler(c); err != nil {
		t.Fatal(err)
	}
	var answer map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &answer); err != nil {
		t.Fatalf("%s: %v", rec.Body.String(), err)
	}
	return rec.Code, answer
}

// On the machine the tests run on there is no ZFS: the answer is a quiet 200, not an error.
func TestGetZFSWithoutZFS(t *testing.T) {
	code, answer := zfsCall(t, GetZFS, "/v1/disks/zfs")
	data, _ := answer["data"].(map[string]interface{})
	if code != http.StatusOK || data["available"] != false || data["reason"] == "" {
		t.Fatalf("%d %v", code, answer)
	}
	if pools, ok := data["pools"].([]interface{}); !ok || len(pools) != 0 {
		t.Fatalf("pools must be an empty list, not null: %v", data["pools"])
	}
}

func TestGetZFSSnapshotsOfADatasetThatIsNotThere(t *testing.T) {
	code, _ := zfsCall(t, GetZFSSnapshots, "/v1/disks/zfs/snapshots?dataset=-r")
	if code != http.StatusConflict && code != http.StatusNotFound {
		t.Fatalf("%d", code)
	}
}

func TestGetZFSIO(t *testing.T) {
	dir := t.TempDir()
	saved := zfsKstat
	zfsKstat = dir
	t.Cleanup(func() { zfsKstat = saved })
	if err := os.MkdirAll(filepath.Join(dir, "tank"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tank", "io"), []byte("1 1 0x01 4 1 1 1\nnread nwritten reads writes\n10 20 3 4\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	code, answer := zfsCall(t, GetZFSIO, "/v1/disks/zfs/io")
	list, _ := answer["data"].([]interface{})
	if code != http.StatusOK || len(list) != 1 {
		t.Fatalf("%d %v", code, answer)
	}
	pool := list[0].(map[string]interface{})
	if pool["pool"] != "tank" || pool["read_bytes"] != float64(10) || pool["write_ops"] != float64(4) {
		t.Fatalf("%v", pool)
	}
}
