package route

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/ReCasaOS/CasaOS-Common/external"
	"github.com/ReCasaOS/CasaOS-Common/utils/jwt"
	"github.com/ReCasaOS/CasaOS-LocalStorage/pkg/config"
)

// The access token bare (as the dashboard sends it) or as `Bearer <token>`; another scheme, or
// the scheme alone, is no token. v1: header or ?token=, the raw header decides. Past the token, a
// path that is no route answers 404: what is checked here is the token alone.
func TestTheAccessTokenIsTakenBareOrAsBearer(t *testing.T) {
	runtime := t.TempDir()
	previous := config.CommonInfo.RuntimePath
	config.CommonInfo.RuntimePath = runtime
	t.Cleanup(func() { config.CommonInfo.RuntimePath = previous })
	if err := os.WriteFile(filepath.Join(runtime, external.InternalSecretFilename), []byte("s3cret"), 0o600); err != nil {
		t.Fatal(err)
	}
	privateKey, publicKey, err := jwt.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	jwks, err := jwt.GenerateJwksJSON(publicKey)
	if err != nil {
		t.Fatal(err)
	}
	userService := httptest.NewServer(jwt.JWKSHandler(jwks))
	defer userService.Close()
	if err := os.WriteFile(filepath.Join(runtime, external.UserServiceAddressFilename), []byte(userService.URL), 0o600); err != nil {
		t.Fatal(err)
	}
	token, err := jwt.GetAccessToken("admin", privateKey, 1)
	if err != nil {
		t.Fatal(err)
	}

	v1, v2 := InitV1Router(), InitV2Router()
	for _, c := range []struct {
		name, authorization, query string
		v2                         bool
		passes                     bool
	}{
		{"v1 bare", token, "", false, true},
		{"v1 Bearer", "Bearer " + token, "", false, true},
		{"v1 bearer", "bearer " + token, "", false, true},
		{"v1 Basic", "Basic " + token, "", false, false},
		{"v1 the scheme alone", "Bearer", "", false, false},
		{"v1 ?token= without a header", "", token, false, true},
		{"v1 the scheme alone and ?token=: the header decides", "Bearer", token, false, false},
		{"v2 bare", token, "", true, true},
		{"v2 Bearer", "Bearer " + token, "", true, true},
		{"v2 Basic", "Basic " + token, "", true, false},
		{"v2 the scheme alone", "Bearer", "", true, false},
	} {
		target, router := "/v1/disks/nothing-here", v1
		if c.v2 {
			target, router = "/v2/local_storage/nothing-here", v2
		}
		if c.query != "" {
			target += "?token=" + c.query
		}
		request := httptest.NewRequest(http.MethodGet, target, nil)
		request.RemoteAddr = "203.0.113.5:40000"
		if c.authorization != "" {
			request.Header.Set("Authorization", c.authorization)
		}
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, request)
		if passed := recorder.Code != http.StatusUnauthorized; passed != c.passes {
			t.Errorf("%s: %d, passes %v, want %v", c.name, recorder.Code, passed, c.passes)
		}
	}
}
