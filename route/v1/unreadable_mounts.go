package v1

import (
	"net/http"

	"github.com/ReCasaOS/CasaOS-Common/model"
	"github.com/ReCasaOS/CasaOS-Common/utils/common_err"
	"github.com/labstack/echo/v4"

	"github.com/ReCasaOS/CasaOS-LocalStorage/service"
)

// GetUnreadableMounts lists the mounts df cannot read (a dead NFS share, a FUSE mount whose program died), for "Needs attention".
func GetUnreadableMounts(ctx echo.Context) error {
	mounts, err := service.UnreadableMounts()
	if err != nil {
		return ctx.JSON(http.StatusInternalServerError, model.Result{Success: common_err.SERVICE_ERROR, Message: err.Error()})
	}
	return ctx.JSON(common_err.SUCCESS, model.Result{Success: common_err.SUCCESS, Message: common_err.GetMsg(common_err.SUCCESS), Data: mounts})
}
