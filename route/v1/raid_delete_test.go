package v1

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ReCasaOS/CasaOS-Common/utils/logger"
	"github.com/labstack/echo/v4"
)

func deleteRAIDArray(t *testing.T, name, body string) int {
	t.Helper()
	logger.LogInitConsoleOnly()
	request := httptest.NewRequest(http.MethodDelete, "/v1/disks/raid/"+name, strings.NewReader(body))
	request.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	ctx := echo.New().NewContext(request, rec)
	ctx.SetParamNames("name")
	ctx.SetParamValues(name)
	if err := DeleteRAIDArray(ctx); err != nil {
		t.Fatalf("%s: %v", body, err)
	}
	return rec.Code
}

// The services are not set up here: a request that went past these checks would panic.
func TestAnArrayIsNotDeletedWithoutItsNameTypedAgain(t *testing.T) {
	for _, body := range []string{``, `{}`, `{"confirm": ""}`, `{"confirm": "md1"}`, `{"confirm": 0}`} {
		if code := deleteRAIDArray(t, "md0", body); code != http.StatusBadRequest {
			t.Errorf("%q: status %d, want 400", body, code)
		}
	}
}

func TestAnArrayThatDoesNotExistIsNotFound(t *testing.T) {
	if code := deleteRAIDArray(t, "md999", `{"confirm": "md999"}`); code != http.StatusNotFound {
		t.Errorf("status %d, want 404", code)
	}
}
