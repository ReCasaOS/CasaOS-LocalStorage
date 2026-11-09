package v1

import (
	"errors"
	"net/http"

	"github.com/ReCasaOS/CasaOS-Common/model"
	"github.com/ReCasaOS/CasaOS-Common/utils/common_err"
	"github.com/ReCasaOS/CasaOS-LocalStorage/service"
	"github.com/labstack/echo/v4"
)

// zfsKstat is where the kernel keeps a folder of counters for each pool, a var for the tests.
var zfsKstat = "/proc/spl/kstat/zfs"

// GetZFS is the ZFS pools and datasets, read only. A box without ZFS answers 200 with available false and the reason, so that the dashboard keeps quiet.
func GetZFS(ctx echo.Context) error {
	return ctx.JSON(common_err.SUCCESS, model.Result{Success: common_err.SUCCESS, Message: common_err.GetMsg(common_err.SUCCESS), Data: service.ZFSOverview()})
}

// GetZFSSnapshots lists the snapshots of the dataset in ?dataset=, the newest first. A dataset the box does not list is 404 and nothing is run for it.
func GetZFSSnapshots(ctx echo.Context) error {
	list, err := service.ZFSSnapshotsOf(ctx.QueryParam("dataset"))
	if err != nil {
		status := http.StatusInternalServerError
		switch {
		case errors.Is(err, service.ErrZFSNoDataset):
			status = http.StatusNotFound
		case errors.Is(err, service.ErrZFSUnavailable):
			status = http.StatusConflict
		case errors.Is(err, service.ErrZFSBusy):
			status = http.StatusServiceUnavailable
		}
		return ctx.JSON(status, model.Result{Success: common_err.SERVICE_ERROR, Message: err.Error(), Data: err.Error()})
	}
	return ctx.JSON(common_err.SUCCESS, model.Result{Success: common_err.SUCCESS, Message: common_err.GetMsg(common_err.SUCCESS), Data: list})
}

// GetZFSIO is the counters of every pool (bytes and operations since it was imported), read from the kernel without running anything: the rate is the
// difference between two readings.
func GetZFSIO(ctx echo.Context) error {
	return ctx.JSON(common_err.SUCCESS, model.Result{Success: common_err.SUCCESS, Message: common_err.GetMsg(common_err.SUCCESS), Data: service.ZFSIO(zfsKstat)})
}
