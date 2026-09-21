package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/ReCasaOS/CasaOS-Common/external"
	"github.com/ReCasaOS/CasaOS-LocalStorage/codegen/message_bus"
	"github.com/ReCasaOS/CasaOS-LocalStorage/pkg/config"
)

// The message bus refuses loopback calls without this boot's secret, so the
// generated client has to send it like Common's own clients do.
func TestMessageBusSendsTheInternalSecret(t *testing.T) {
	received := make(chan string, 1)
	bus := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received <- r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer bus.Close()

	runtimePath := t.TempDir()
	for name, content := range map[string]string{
		external.MessageBusAddressFilename: bus.URL,
		external.InternalSecretFilename:    "s3cret\n",
	} {
		if err := os.WriteFile(filepath.Join(runtimePath, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	previous := config.CommonInfo.RuntimePath
	config.CommonInfo.RuntimePath = runtimePath
	defer func() { config.CommonInfo.RuntimePath = previous }()

	response, err := (&store{}).MessageBus().RegisterEventTypes(context.Background(), []message_bus.EventType{})
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()

	if authorization := <-received; !external.IsInternalRequest("127.0.0.1", authorization, runtimePath) {
		t.Fatalf("the bus got Authorization %q, not the internal secret", authorization)
	}
}
