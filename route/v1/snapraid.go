package v1

import (
	"errors"
	"net/http"
	"strings"

	"github.com/ReCasaOS/CasaOS-Common/model"
	"github.com/ReCasaOS/CasaOS-Common/utils/common_err"
	"github.com/ReCasaOS/CasaOS-LocalStorage/service"
	"github.com/labstack/echo/v4"
)

var ErrSnapRAIDNoPool = errors.New("no such pool of merged storages, or it holds no disk")

// snapraidPoolDisks are the mount points of the disks a mergerfs pool is made of, a var for the tests.
var snapraidPoolDisks = func(pool string) ([]string, error) {
	merges, err := service.MyService.LocalStorage().GetMerges(&pool)
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, m := range merges {
		for _, v := range m.SourceVolumes {
			if v != nil && v.MountPoint != "" {
				paths = append(paths, v.MountPoint)
			}
		}
	}
	if len(paths) == 0 {
		return nil, ErrSnapRAIDNoPool
	}
	return paths, nil
}

// snapraidAnswer answers an error of SnapRAID: what asks for something that is not there as 404, what cannot be done now as 409, what was asked wrongly as 400,
// the rest as 500. The reason is in Message and Data, for the dashboard to show.
func snapraidAnswer(ctx echo.Context, err error) error {
	status := http.StatusInternalServerError
	switch {
	case errors.Is(err, service.ErrSnapRAIDNotSet), errors.Is(err, ErrSnapRAIDNoPool):
		status = http.StatusNotFound
	case errors.Is(err, service.ErrSnapRAIDBusy), errors.Is(err, service.ErrNoSnapRAID), errors.Is(err, service.ErrSnapRAIDNotMounted):
		status = http.StatusConflict
	case errors.Is(err, service.ErrSnapRAIDParity), errors.Is(err, service.ErrSnapRAIDData), errors.Is(err, service.ErrSnapRAIDSameDisk),
		errors.Is(err, service.ErrSnapRAIDParitySize), errors.Is(err, service.ErrSnapRAIDAction), errors.Is(err, service.ErrSnapRAIDConsent), errors.Is(err, service.ErrSnapRAIDNoDisk):
		status = http.StatusBadRequest
	}
	return ctx.JSON(status, model.Result{Success: common_err.SERVICE_ERROR, Message: err.Error(), Data: err.Error()})
}

// GetSnapRAID is how things stand: whether it is set up, what is at work, how each action ended last time, the disks that are not where they should be.
func GetSnapRAID(ctx echo.Context) error {
	return ctx.JSON(common_err.SUCCESS, model.Result{Success: common_err.SUCCESS, Message: common_err.GetMsg(common_err.SUCCESS), Data: service.SnapRAIDStatus()})
}

// PutSnapRAID sets up the protection, or changes it: the data disks (the disks of a pool, named by its mount point, or a list of mount points) and one or two
// parity disks, which must be mounted storages of their own. Nothing is written to the disks yet; the first sync does it.
// {"pool": "/media/pool", "parity": ["/media/Parity"], "max_removed": 40, "hour": 3}
func PutSnapRAID(ctx echo.Context) error {
	var request struct {
		Pool       string   `json:"pool"`
		Data       []string `json:"data"`
		Parity     []string `json:"parity"`
		MaxRemoved *int     `json:"max_removed"`
		Hour       *int     `json:"hour"`
	}
	if err := ctx.Bind(&request); err != nil {
		return ctx.JSON(http.StatusBadRequest, model.Result{Success: common_err.INVALID_PARAMS, Message: err.Error(), Data: err.Error()})
	}
	data := request.Data
	if request.Pool != "" {
		paths, err := snapraidPoolDisks(request.Pool)
		if err != nil {
			return snapraidAnswer(ctx, err)
		}
		data = paths
	}
	cfg, err := service.SetSnapRAID(data, request.Parity, request.MaxRemoved, request.Hour)
	if err != nil {
		return snapraidAnswer(ctx, err)
	}
	return ctx.JSON(common_err.SUCCESS, model.Result{Success: common_err.SUCCESS, Message: common_err.GetMsg(common_err.SUCCESS), Data: cfg})
}

// DeleteSnapRAID forgets the setup. The parity files stay on the parity disks.
func DeleteSnapRAID(ctx echo.Context) error {
	if err := service.ClearSnapRAID(); err != nil {
		return snapraidAnswer(ctx, err)
	}
	return ctx.JSON(common_err.SUCCESS, model.Result{Success: common_err.SUCCESS, Message: common_err.GetMsg(common_err.SUCCESS)})
}

// PostSnapRAIDAction starts diff, sync, scrub or fix and answers at once (202); GetSnapRAID follows it. Options in the body:
// sync {"force": true} goes on although many files were removed; scrub {"full": true} reads all of the array; fix {"disk": "d1", "confirm": "d1"} (or
// {"confirm": "all"}) puts files back as they were at the last sync, which it first needs to be told twice.
func PostSnapRAIDAction(ctx echo.Context) error {
	var request struct {
		Force   bool   `json:"force"`
		Full    bool   `json:"full"`
		Disk    string `json:"disk"`
		Confirm string `json:"confirm"`
	}
	// no body at all is fine: a sync needs none
	if ctx.Request().ContentLength != 0 {
		if err := ctx.Bind(&request); err != nil {
			return ctx.JSON(http.StatusBadRequest, model.Result{Success: common_err.INVALID_PARAMS, Message: err.Error(), Data: err.Error()})
		}
	}
	job, err := service.StartSnapRAID(strings.ToLower(ctx.Param("action")), service.SnapRAIDOptions{Force: request.Force, Full: request.Full, Disk: request.Disk, Confirm: request.Confirm})
	if err != nil {
		return snapraidAnswer(ctx, err)
	}
	return ctx.JSON(http.StatusAccepted, model.Result{Success: common_err.SUCCESS, Message: common_err.GetMsg(common_err.SUCCESS), Data: job})
}

// DeleteSnapRAIDJob stops the action at work; SnapRAID saves where it was, and a later sync takes up from there.
func DeleteSnapRAIDJob(ctx echo.Context) error {
	if !service.StopSnapRAID() {
		return ctx.JSON(http.StatusNotFound, model.Result{Success: common_err.SERVICE_ERROR, Message: "nothing is at work", Data: "nothing is at work"})
	}
	return ctx.JSON(common_err.SUCCESS, model.Result{Success: common_err.SUCCESS, Message: common_err.GetMsg(common_err.SUCCESS)})
}
