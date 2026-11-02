package v2

import (
	"net/http"
	"strings"

	"github.com/ReCasaOS/CasaOS-Common/utils/logger"
	"github.com/ReCasaOS/CasaOS-LocalStorage/codegen"
	"github.com/ReCasaOS/CasaOS-LocalStorage/common"
	"github.com/ReCasaOS/CasaOS-LocalStorage/pkg/config"
	"github.com/ReCasaOS/CasaOS-LocalStorage/pkg/utils/merge"
	"github.com/ReCasaOS/CasaOS-LocalStorage/service"
	model2 "github.com/ReCasaOS/CasaOS-LocalStorage/service/model"
	"github.com/ReCasaOS/CasaOS-LocalStorage/service/v2/fs"
	"go.uber.org/zap"

	"github.com/labstack/echo/v4"
)

var MessageMergerFSNotEnabled = "mergerfs is not enabled - either it is not enabled in configuration file; merge point is not empty before mounting; or mergerfs is not installed"

func (s *LocalStorage) GetMerges(ctx echo.Context, params codegen.GetMergesParams) error {
	if strings.ToLower(config.ServerInfo.EnableMergerFS) != "true" {
		return ctx.JSON(http.StatusServiceUnavailable, codegen.ResponseServiceUnavailable{Message: &MessageMergerFSNotEnabled})
	}

	merges, err := service.MyService.LocalStorage().GetMerges(params.MountPoint)
	if err != nil {
		message := err.Error()
		return ctx.JSON(http.StatusInternalServerError, codegen.BaseResponse{Message: &message})
	}
	data := make([]codegen.Merge, 0, len(merges))
	for _, merge := range merges {
		data = append(data, MergeAdapterOut(merge))
	}
	message := "ok"
	return ctx.JSON(http.StatusOK, codegen.GetMergesResponseOK{Data: &data, Message: &message})

}

func (s *LocalStorage) SetMerge(ctx echo.Context) error {
	var m codegen.Merge
	if err := ctx.Bind(&m); err != nil {
		message := err.Error()
		return ctx.JSON(http.StatusBadRequest, codegen.ResponseBadRequest{Message: &message})
	}

	// An explicitly empty source list is the full-unmerge operation for the
	// merge at that mount point. This is deliberately different from an omitted source
	// list, which leaves the current source selection unchanged.
	if isFullMergeRemoval(m) {
		if err := service.MyService.LocalStorage().RemoveMerge(m.MountPoint); err != nil {
			message := err.Error()
			logger.Error("failed to remove merge", zap.Error(err), zap.String("mount point", m.MountPoint))
			return ctx.JSON(http.StatusInternalServerError, codegen.BaseResponse{Message: &message})
		}

		// mergerfs stays enabled while another merge is left
		remaining, err := service.MyService.LocalStorage().GetMergeAllFromDB(nil)
		if err != nil {
			message := err.Error()
			return ctx.JSON(http.StatusInternalServerError, codegen.BaseResponse{Message: &message})
		}
		if len(remaining) == 0 {
			config.ServerInfo.EnableMergerFS = "false"
			if config.Cfg != nil {
				config.Cfg.Section("server").Key("EnableMergerFS").SetValue("false")
				if err := config.Cfg.SaveTo(config.LocalStorageConfigFilePath); err != nil {
					message := err.Error()
					logger.Error("failed to persist mergerfs disabled state", zap.Error(err))
					return ctx.JSON(http.StatusInternalServerError, codegen.BaseResponse{Message: &message})
				}
			}
		}

		const messageStatus = common.ServiceName + ":merge_status"
		msg := map[string]interface{}{
			"mount_point":         m.MountPoint,
			"source_base_path":    "",
			"source_volume_uuids": []string{},
		}
		if err := service.MyService.Notify().SendNotify(messageStatus, msg); err != nil {
			logger.Error("error when sending merge removal notification", zap.Error(err), zap.String("message path", messageStatus), zap.Any("message", msg))
		}

		message := "merge removed"
		return ctx.JSON(http.StatusOK, codegen.SetMergeResponseOK{Message: &message})
	}

	// default to mergerfs if fstype is not specified
	fstype := fs.MergerFSFullName
	if m.Fstype != nil {
		fstype = *m.Fstype
	}
	// expand source volume paths to source volumes
	var sourceVolumes []*model2.Volume
	if m.SourceVolumeUuids != nil {
		volumesFromDB, err := service.MyService.Disk().GetSerialAllFromDB()
		if err != nil {
			logger.Error("failed to get serial disks from database", zap.Error(err))
			message := err.Error()
			return ctx.JSON(http.StatusInternalServerError, codegen.BaseResponse{Message: &message})
		}

		sourceVolumes = make([]*model2.Volume, 0, len(*m.SourceVolumeUuids))
		for _, volumeUUID := range *m.SourceVolumeUuids {
			volumeFound := false
			for i := range volumesFromDB {
				if volumeUUID == volumesFromDB[i].UUID {
					volumeFound = true
					sourceVolumes = append(sourceVolumes, &volumesFromDB[i])
					break
				}
			}

			if !volumeFound {
				message := "volume " + volumeUUID + " not found, or it is not a CasaOS storage. Consider adding it to CasaOS first."
				return ctx.JSON(http.StatusBadRequest, codegen.ResponseBadRequest{Message: &message})
			}
		}
	}

	merge, err := service.MyService.LocalStorage().GetFirstMergeFromDB(m.MountPoint)
	if err != nil {
		message := err.Error()
		logger.Error("failed to get merge from database", zap.Error(err), zap.String("mount point", m.MountPoint))
		return ctx.JSON(http.StatusInternalServerError, codegen.BaseResponse{Message: &message})
	}

	if merge == nil {
		merge = &model2.Merge{
			FSType:     fstype,
			MountPoint: m.MountPoint,
		}
		applyMergeSources(merge, m.SourceBasePath, m.SourceVolumeUuids, sourceVolumes)

		if err := service.MyService.LocalStorage().CreateMerge(merge); err != nil {

			message := err.Error()
			logger.Error("failed to create merge", zap.Error(err), zap.String("mount point", m.MountPoint))
			return ctx.JSON(http.StatusInternalServerError, codegen.BaseResponse{Message: &message})
		}

		if err := service.MyService.LocalStorage().CreateMergeInDB(merge); err != nil {
			logger.Error("failed to create merge in database", zap.Error(err), zap.String("mount point", m.MountPoint))
			message := err.Error()
			return ctx.JSON(http.StatusInternalServerError, codegen.BaseResponse{Message: &message})
		}
	} else {
		applyMergeSources(merge, m.SourceBasePath, m.SourceVolumeUuids, sourceVolumes)

		if err := service.MyService.LocalStorage().UpdateMerge(merge); err != nil {
			message := err.Error()
			logger.Error("failed to update merge", zap.Error(err), zap.String("mount point", m.MountPoint))
			return ctx.JSON(http.StatusInternalServerError, codegen.BaseResponse{Message: &message})
		}

		if err := service.MyService.LocalStorage().UpdateMergeSourcesInDB(merge); err != nil {
			message := err.Error()
			logger.Error("failed to update merge sources in database", zap.Error(err), zap.String("mount point", m.MountPoint))
			return ctx.JSON(http.StatusInternalServerError, codegen.BaseResponse{Message: &message})
		}
	}
	if err := enableMergerFSConfig(); err != nil {
		message := err.Error()
		logger.Error("failed to persist mergerfs enabled state", zap.Error(err))
		return ctx.JSON(http.StatusInternalServerError, codegen.BaseResponse{Message: &message})
	}
	const messageStatus = common.ServiceName + ":merge_status"
	result := MergeAdapterOut(*merge)
	msg := make(map[string]interface{})
	msg["mount_point"] = result.MountPoint
	msg["source_base_path"] = result.SourceBasePath
	msg["source_volume_uuids"] = result.SourceVolumeUuids
	msg["fs_type"] = result.Fstype
	msg["created_at"] = result.CreatedAt
	msg["updated_at"] = result.UpdatedAt

	if err := service.MyService.Notify().SendNotify(messageStatus, msg); err != nil {
		logger.Error("error when sending notification", zap.Error(err), zap.String("message path", messageStatus), zap.Any("message", msg))
	}

	return ctx.JSON(http.StatusOK, codegen.SetMergeResponseOK{
		Data: &result,
	})
}

func applyMergeSources(merge *model2.Merge, sourceBasePath *string, sourceVolumeUuids *[]string, sourceVolumes []*model2.Volume) {
	if sourceBasePath != nil {
		if strings.TrimSpace(*sourceBasePath) == "" {
			merge.SourceBasePath = nil
		} else {
			merge.SourceBasePath = sourceBasePath
		}
	} else if sourceVolumeUuids != nil {
		// An explicit volume list represents the complete source selection. Do
		// not retain the system-storage bootstrap path unless it is requested.
		merge.SourceBasePath = nil
	}

	if sourceVolumeUuids != nil {
		merge.SourceVolumes = sourceVolumes
	}
}

func isFullMergeRemoval(merge codegen.Merge) bool {
	if merge.MountPoint == "" || merge.SourceVolumeUuids == nil || len(*merge.SourceVolumeUuids) != 0 {
		return false
	}
	return merge.SourceBasePath == nil || strings.TrimSpace(*merge.SourceBasePath) == ""
}

func enableMergerFSConfig() error {
	config.ServerInfo.EnableMergerFS = "true"
	if config.Cfg == nil {
		return nil
	}

	config.Cfg.Section("server").Key("EnableMergerFS").SetValue("true")
	return config.Cfg.SaveTo(config.LocalStorageConfigFilePath)
}

func (s *LocalStorage) GetMergeInitStatus(ctx echo.Context) error {
	status := codegen.Uninitialized
	mountPoint := common.DefaultMountPoint

	existingMerges, err := service.MyService.LocalStorage().GetMergeAllFromDB(&mountPoint)
	if err != nil {
		message := err.Error()
		return ctx.JSON(http.StatusInternalServerError, codegen.BaseResponse{Message: &message})
	}

	// check if /DATA is already a merge point
	if len(existingMerges) == 0 {
		return ctx.JSON(http.StatusOK, codegen.GetMergeInitStatusResponseOK{Data: &status})
	}

	status = codegen.Initialized
	if strings.ToLower(config.ServerInfo.EnableMergerFS) == "true" {
		merge := &existingMerges[0]
		if !service.MyService.LocalStorage().IsMergeMounted(merge.MountPoint, merge.FSType) {
			status = codegen.Error
		}
	}

	if status != codegen.Initialized {
		message := service.MyService.LocalStorage().LastMergeRestoreError(mountPoint)
		if len(message) == 0 {
			message = "merge is configured but not mounted; the most recent restore failed or has not run yet"
		}
		return ctx.JSON(http.StatusOK, codegen.GetMergeInitStatusResponseOK{Data: &status, Message: &message})
	}

	return ctx.JSON(http.StatusOK, codegen.GetMergeInitStatusResponseOK{Data: &status})
}
func (s *LocalStorage) InitMerge(ctx echo.Context) error {
	var m codegen.MountPoint
	if err := ctx.Bind(&m); err != nil {
		message := err.Error()
		return ctx.JSON(http.StatusBadRequest, codegen.ResponseBadRequest{Message: &message})
	}

	if m.MountPoint == "" {
		message := "mount point is empty"
		return ctx.JSON(http.StatusBadRequest, codegen.ResponseBadRequest{Message: &message})
	}
	if strings.ToLower(config.ServerInfo.EnableMergerFS) != "true" {
		if err := service.MyService.LocalStorage().PrepareExternalDataRoot(m.MountPoint); err != nil {
			message := err.Error()
			return ctx.JSON(http.StatusBadRequest, codegen.ResponseBadRequest{Message: &message})
		}

		if !merge.IsMergerFSInstalled() {
			config.ServerInfo.EnableMergerFS = "false"
			message := "mergerfs is not installed"
			return ctx.JSON(http.StatusBadRequest, codegen.ResponseBadRequest{Message: &message})
		}

		// The actual mergerfs mount is created by SetMerge from the selected
		// external volumes. Do not create the historical system-backed bootstrap
		// merge here, otherwise the flash/system disk is briefly part of /DATA.
	} else {
		status := codegen.Initialized
		return ctx.JSON(http.StatusOK, codegen.InitMergeResponseOK{Data: &status})
	}
	status := codegen.Initialized
	return ctx.JSON(http.StatusOK, codegen.InitMergeResponseOK{Data: &status})
}

func MergeAdapterOut(m model2.Merge) codegen.Merge {
	id := int(m.ID)

	sourceVolumeUUIDs := make([]string, 0, len(m.SourceVolumes))
	for _, volume := range m.SourceVolumes {
		sourceVolumeUUIDs = append(sourceVolumeUUIDs, volume.UUID)
	}

	return codegen.Merge{
		Id:                &id,
		Fstype:            &m.FSType,
		MountPoint:        m.MountPoint,
		SourceBasePath:    m.SourceBasePath,
		SourceVolumeUuids: &sourceVolumeUUIDs,
		CreatedAt:         &m.CreatedAt,
		UpdatedAt:         &m.UpdatedAt,
	}
}
