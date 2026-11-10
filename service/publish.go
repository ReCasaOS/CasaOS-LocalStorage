package service

import (
	"context"
	"net/http"

	"github.com/ReCasaOS/CasaOS-Common/utils/logger"
	"github.com/ReCasaOS/CasaOS-LocalStorage/codegen/message_bus"
	"go.uber.org/zap"
)

// PublishEvent hands an event to the message bus. The bus is not something this service needs
// to run: when it does not answer there is no response to read, and the event is lost, no more.
func PublishEvent(ctx context.Context, event *message_bus.Event) {
	response, err := MyService.MessageBus().PublishEventWithResponse(ctx, event.SourceID, event.Name, event.Properties)
	if err != nil {
		logger.Error("failed to publish event to message bus", zap.Error(err), zap.Any("event", event))
		return
	}

	if response.StatusCode() != http.StatusOK {
		logger.Error("failed to publish event to message bus", zap.String("status", response.Status()), zap.Any("response", response))
	}
}
