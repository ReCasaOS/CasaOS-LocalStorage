package service

import (
	"context"
	"testing"

	"github.com/ReCasaOS/CasaOS-Common/utils/logger"
	"github.com/ReCasaOS/CasaOS-LocalStorage/codegen/message_bus"
	"github.com/ReCasaOS/CasaOS-LocalStorage/pkg/config"
)

// The message bus is not something the storage service needs to run. A disk plugged in
// while the bus is stopped is an event nobody hears, not the end of the service.
func TestADiskPluggedInWhileTheMessageBusIsStoppedDoesNotStopTheService(t *testing.T) {
	logger.LogInitConsoleOnly()
	previous, services := config.CommonInfo.RuntimePath, MyService
	config.CommonInfo.RuntimePath = t.TempDir() // no address of a bus in it
	MyService = &store{}
	t.Cleanup(func() { config.CommonInfo.RuntimePath, MyService = previous, services })

	PublishEvent(context.Background(), &message_bus.Event{
		SourceID:   "local-storage",
		Name:       "local-storage:disk:added",
		Properties: map[string]string{"local-storage:path": "/dev/sdb"},
	})
}
