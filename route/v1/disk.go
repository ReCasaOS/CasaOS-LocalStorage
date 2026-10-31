package v1

import (
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/ReCasaOS/CasaOS-Common/model"
	"github.com/ReCasaOS/CasaOS-Common/utils/common_err"
	"github.com/ReCasaOS/CasaOS-Common/utils/logger"
	"github.com/ReCasaOS/CasaOS-LocalStorage/common"
	model1 "github.com/ReCasaOS/CasaOS-LocalStorage/model"
	"github.com/ReCasaOS/CasaOS-LocalStorage/pkg/config"
	"github.com/ReCasaOS/CasaOS-LocalStorage/service"
	"github.com/labstack/echo/v4"
	"github.com/shirou/gopsutil/v3/disk"
	"go.uber.org/zap"
)

const messagePathStorageStatus = common.ServiceName + ":storage_status"

var diskMap = make(map[string]string)

type StorageMessage struct {
	Type   string `json:"type"`   // sata,usb
	Action string `json:"action"` // remove add
	Path   string `json:"path"`
	Volume string `json:"volume"`
	Size   uint64 `json:"size"`
}

// @Summary disk list
// @Produce  application/json
// @Accept application/json
// @Tags disk
// @Security ApiKeyAuth
// @Success 200 {string} string "ok"
// @Router /disk/list [get]
func GetDiskList(ctx echo.Context) error {
	blkList := service.MyService.Disk().LSBLK(false)

	dbList, err := service.MyService.Disk().GetSerialAllFromDB()
	if err != nil {
		logger.Error("error when getting all volumes from database", zap.Error(err))
		return ctx.JSON(http.StatusInternalServerError, model.Result{Success: common_err.SERVICE_ERROR, Message: err.Error()})
	}

	part := make(map[string]int64, len(dbList))
	for _, v := range dbList {
		part[v.MountPoint] = v.CreatedAt
	}

	disks := []model1.Drive{}
	avail := []model1.Drive{}

	var systemDisk *model1.LSBLKModel

	for _, currentDisk := range blkList {
		childre := []model1.DiskChildren{}
		supported := true
		if len(currentDisk.Children) > 0 {
			for _, v := range currentDisk.Children {
				if !service.IsFormatSupported(v) {
					supported = false
				}
				t := model1.DiskChildren{
					Name:      v.Name,
					Size:      v.Size,
					Format:    v.FsType,
					Supported: service.IsFormatSupported(v),
				}
				childre = append(childre, t)
			}
		} else {
			if !service.IsFormatSupported(currentDisk) {
				supported = false
			}
		}

		disk := model1.Drive{
			Serial:         currentDisk.Serial,
			Name:           currentDisk.Name,
			Size:           currentDisk.Size,
			Path:           currentDisk.Path,
			Model:          currentDisk.Model,
			ChildrenNumber: len(currentDisk.Children),
			Children:       childre,
			Supported:      supported,
		}

		if currentDisk.Rota {
			disk.DiskType = "HDD"
		} else {
			disk.DiskType = "SSD"
		}
		if currentDisk.Tran == "usb" {
			disk.DiskType = "USB"
		}

		temp := service.MyService.Disk().SmartCTL(currentDisk.Path)
		disk.Temperature = temp.Temperature.Current
		disk.SmartStatus = temp.SmartHealth()
		disk.Health = strconv.FormatBool(disk.SmartStatus != model1.SmartHealthFailed)

		if systemDisk == nil {
			// go 5 level deep to look for system block device by mount point being "/"
			systemDisk := service.WalkDisk(currentDisk, 5, func(blk model1.LSBLKModel) bool { return blk.MountPoint == "/" })

			if systemDisk != nil {
				disk.Model = "System"
				if strings.Contains(systemDisk.SubSystems, "mmc") {
					disk.DiskType = "MMC"
				} else if strings.Contains(systemDisk.SubSystems, "usb") {
					disk.DiskType = "USB"
				}
				disks = append(disks, disk)
				continue
			}
		}

		if !service.IsDiskSupported(currentDisk) {
			continue
		}

		// Only direct children used to be looked at for a mount point, so a disk
		// whose partitions were RAID members, or held a volume group, was offered
		// for formatting.
		if !service.DiskInUse(currentDisk) {
			disk.NeedFormat = false
			avail = append(avail, disk)
		}

		disks = append(disks, disk)
	}

	data := map[string]interface{}{
		"disks": disks,
		"avail": avail,
	}

	return ctx.JSON(common_err.SUCCESS, model.Result{Success: common_err.SUCCESS, Message: common_err.GetMsg(common_err.SUCCESS), Data: data})
}

// @Summary disk list
// @Produce  application/json
// @Accept application/json
// @Tags disk
// @Security ApiKeyAuth
// @Success 200 {string} string "ok"
// @Router /disk/list [get]

func DeleteDisksUmount(ctx echo.Context) error {
	js := make(map[string]string)
	if err := ctx.Bind(&js); err != nil {
		return ctx.JSON(http.StatusBadRequest, model.Result{Success: common_err.INVALID_PARAMS, Message: common_err.GetMsg(common_err.INVALID_PARAMS), Data: err.Error()})
	}

	// requires password from user to confirm the action
	// if claims, err := jwt.ParseToken(c.GetHeader("Authorization"), false); err != nil || encryption.GetMD5ByStr(js["password"]) != claims.Password {
	// 	return ctx.JSON(http.StatusUnauthorized, model.Result{Success: common_err.PWD_INVALID, Message: common_err.GetMsg(common_err.PWD_INVALID)})
	// 	return
	// }

	path := js["path"]

	if len(path) == 0 {
		return ctx.JSON(common_err.CLIENT_ERROR, model.Result{Success: common_err.INVALID_PARAMS, Message: common_err.GetMsg(common_err.INVALID_PARAMS)})
	}

	if _, ok := diskMap[path]; ok {
		return ctx.JSON(common_err.SERVICE_ERROR, model.Result{Success: common_err.DISK_BUSYING, Message: common_err.GetMsg(common_err.DISK_BUSYING)})
	}

	diskInfo := service.MyService.Disk().GetDiskInfo(path)
	if len(diskInfo.Children) == 0 && service.IsDiskSupported(diskInfo) {
		t := diskInfo
		t.Children = nil
		diskInfo.Children = append(diskInfo.Children, t)
	}
	for _, v := range diskInfo.Children {
		if err := service.MyService.Disk().UmountPointAndRemoveDir(v); err != nil {
			return ctx.JSON(http.StatusInternalServerError, model.Result{Success: common_err.REMOVE_MOUNT_POINT_ERROR, Message: err.Error()})
		}

		// delete data
		if err := service.MyService.Disk().DeleteMountPointFromDB(v.Path, v.MountPoint); err != nil {
			logger.Error("error when deleting mount point from database", zap.Error(err), zap.String("path", v.Path), zap.String("mount point", v.MountPoint))
		}

		if err := service.MyService.Shares().DeleteShare(v.MountPoint); err != nil {
			logger.Error("error when deleting share by mount point", zap.Error(err), zap.String("mount point", v.MountPoint))
		}
	}

	service.MyService.Disk().RemoveLSBLKCache()

	// send notify to client
	go func() {
		message := map[string]interface{}{
			"data": StorageMessage{
				Action: "REMOVED",
				Path:   path,
				Volume: "",
				Size:   0,
				Type:   "",
			},
		}

		if err := service.MyService.Notify().SendNotify(messagePathStorageStatus, message); err != nil {
			logger.Error("error when sending notification", zap.Error(err), zap.String("message path", messagePathStorageStatus), zap.Any("message", message))
		}
	}()

	return ctx.JSON(common_err.SUCCESS, model.Result{Success: common_err.SUCCESS, Message: common_err.GetMsg(common_err.SUCCESS), Data: path})
}

// GetRAIDArrays lists the software RAID arrays (md) and the state of each, as
// the kernel shows them: read only.
func GetRAIDArrays(ctx echo.Context) error {
	return ctx.JSON(common_err.SUCCESS, model.Result{Success: common_err.SUCCESS, Message: common_err.GetMsg(common_err.SUCCESS), Data: service.RAIDArrays("/sys")})
}

// raidAnswer answers a refused or failed repair: what the person can fix
// (no such array, a disk that cannot join, mdadm missing) as 400 or 409, the
// rest as 500. The reason is in Message and Data, for the dashboard to show.
func raidAnswer(ctx echo.Context, err error) error {
	status := http.StatusInternalServerError
	switch {
	case errors.Is(err, service.ErrNoRAIDArray):
		status = http.StatusNotFound
	case errors.Is(err, service.ErrRAIDDiskNotFree), errors.Is(err, service.ErrRAIDDiskTooSmall), errors.Is(err, service.ErrRAIDNotFaulty),
		errors.Is(err, service.ErrRAIDLevel), errors.Is(err, service.ErrRAIDTooFewDisks), errors.Is(err, service.ErrRAIDSameDisk), errors.Is(err, service.ErrRAIDConsent),
		errors.Is(err, service.ErrRAIDNotMirror), errors.Is(err, service.ErrRAIDNotMember), errors.Is(err, service.ErrRAIDFilesystem):
		status = http.StatusBadRequest
	case errors.Is(err, service.ErrRAIDWhole), errors.Is(err, service.ErrRAIDNotRunning), errors.Is(err, service.ErrNoMDADM), errors.Is(err, service.ErrRAIDNotSettled):
		status = http.StatusConflict
	}
	return ctx.JSON(status, model.Result{Success: common_err.SERVICE_ERROR, Message: err.Error(), Data: err.Error()})
}

// PostRAIDDisk wipes a blank disk and adds it to an array that lacks one. The
// body names the disk twice, its path and its name typed again by the person
// ({"path": "/dev/sdd", "confirm": "sdd"}): it is erased.
func PostRAIDDisk(ctx echo.Context) error {
	var request struct {
		Path    string `json:"path"`
		Confirm string `json:"confirm"`
	}
	if err := ctx.Bind(&request); err != nil || request.Path == "" || request.Confirm != strings.TrimPrefix(request.Path, "/dev/") {
		return ctx.JSON(http.StatusBadRequest, model.Result{Success: common_err.INVALID_PARAMS, Message: "the disk's name was not typed again", Data: "the disk's name was not typed again"})
	}
	if _, ok := diskMap[request.Path]; ok {
		return ctx.JSON(common_err.SERVICE_ERROR, model.Result{Success: common_err.DISK_BUSYING, Message: common_err.GetMsg(common_err.DISK_BUSYING)})
	}
	diskMap[request.Path] = "busying"
	defer delete(diskMap, request.Path)
	defer service.MyService.Disk().RemoveLSBLKCache()

	array, err := service.RAIDArrayNamed("/sys", ctx.Param("name"))
	if err != nil {
		return raidAnswer(ctx, err)
	}
	disk, err := service.RAIDCandidate(service.MyService.Disk().LSBLK(false), request.Path, array.ComponentSize)
	if err != nil {
		return raidAnswer(ctx, err)
	}
	if err := service.AddRAIDDisk(array, disk); err != nil {
		logger.Error("error when adding a disk to a RAID array", zap.Error(err), zap.String("array", array.Path), zap.String("disk", disk.Path))
		return raidAnswer(ctx, err)
	}
	array, _ = service.RAIDArrayNamed("/sys", array.Name)
	return ctx.JSON(common_err.SUCCESS, model.Result{Success: common_err.SUCCESS, Message: common_err.GetMsg(common_err.SUCCESS), Data: array})
}

// raidBackupFile is where mdadm may keep the copy of the data it cannot move in place during a reshape: on the system disk, never on the array.
func raidBackupFile(array string) string {
	return filepath.Join(config.AppInfo.DBPath, "raid-"+array+".backup")
}

// PostRAIDLevel changes the RAID 1 called :name to a RAID 5, with a third disk that is blank and erased. A mirror of three has no third disk to give: the path is
// then the disk of its last drive, which is taken out, erased and put back as the third (it is told which when another is named). The body names the level and the disk twice,
// its path and its name typed again by the person ({"level": "raid5", "path": "/dev/sdd", "confirm": "sdd"}). It answers once the reshape has started; it goes on
// in the background, and the array reads "reshaping" until it is over. RAID 5 is the only level offered.
func PostRAIDLevel(ctx echo.Context) error {
	var request struct {
		Level   string `json:"level"`
		Path    string `json:"path"`
		Confirm string `json:"confirm"`
	}
	if err := ctx.Bind(&request); err != nil || request.Path == "" || request.Confirm != strings.TrimPrefix(request.Path, "/dev/") {
		return ctx.JSON(http.StatusBadRequest, model.Result{Success: common_err.INVALID_PARAMS, Message: "the disk's name was not typed again", Data: "the disk's name was not typed again"})
	}
	if request.Level != "raid5" {
		return raidAnswer(ctx, service.ErrRAIDLevel)
	}
	if _, ok := diskMap[request.Path]; ok {
		return ctx.JSON(common_err.SERVICE_ERROR, model.Result{Success: common_err.DISK_BUSYING, Message: common_err.GetMsg(common_err.DISK_BUSYING)})
	}
	diskMap[request.Path] = "busying"
	defer delete(diskMap, request.Path)
	defer service.MyService.Disk().RemoveLSBLKCache()

	array, err := service.RAIDArrayNamed("/sys", ctx.Param("name"))
	if err != nil {
		return raidAnswer(ctx, err)
	}
	var change error
	if array.Level == "raid1" && array.Disks == 3 {
		disk, member, err := service.RAIDFreeableDisk(service.MyService.Disk().LSBLK(false), array, request.Path)
		if err != nil {
			return raidAnswer(ctx, err)
		}
		change = service.RAIDTrioToRAID5(array, disk, member, raidBackupFile(array.Name))
	} else {
		disk, err := service.RAIDCandidate(service.MyService.Disk().LSBLK(false), request.Path, array.ComponentSize)
		if err != nil {
			return raidAnswer(ctx, err)
		}
		change = service.RAIDToRAID5(array, disk, raidBackupFile(array.Name))
	}
	if change != nil {
		logger.Error("error when changing a RAID array to RAID 5", zap.Error(change), zap.String("array", array.Path), zap.String("disk", request.Path))
		return raidAnswer(ctx, change)
	}
	array, _ = service.RAIDArrayNamed("/sys", array.Name)
	return ctx.JSON(common_err.SUCCESS, model.Result{Success: common_err.SUCCESS, Message: common_err.GetMsg(common_err.SUCCESS), Data: array})
}

// PostRAIDFilesystem makes the filesystem on the array :name take all the room the array has, which it has more of after a reshape. It answers 409 while the array
// is not healthy and idle, and 400 for a filesystem that is not ext2, ext3 or ext4.
func PostRAIDFilesystem(ctx echo.Context) error {
	array, err := service.RAIDArrayNamed("/sys", ctx.Param("name"))
	if err != nil {
		return raidAnswer(ctx, err)
	}
	if _, ok := diskMap[array.Path]; ok {
		return ctx.JSON(common_err.SERVICE_ERROR, model.Result{Success: common_err.DISK_BUSYING, Message: common_err.GetMsg(common_err.DISK_BUSYING)})
	}
	diskMap[array.Path] = "busying"
	defer delete(diskMap, array.Path)
	defer service.MyService.Disk().RemoveLSBLKCache()

	if err := service.GrowRAIDFilesystem(array, service.MyService.Disk().GetDiskInfo(array.Path).FsType, raidBackupFile(array.Name)); err != nil {
		logger.Error("error when growing the filesystem of a RAID array", zap.Error(err), zap.String("array", array.Path))
		return raidAnswer(ctx, err)
	}
	array, _ = service.RAIDArrayNamed("/sys", array.Name)
	return ctx.JSON(common_err.SUCCESS, model.Result{Success: common_err.SUCCESS, Message: common_err.GetMsg(common_err.SUCCESS), Data: array})
}

// PostRAIDArray makes an array of the blank disks it names, puts a filesystem on it and mounts it as a storage called name, as Create Storage does for one disk.
// The disks are erased: the body repeats the name ({"name": "Data", "confirm": "Data", "level": "raid1", "disks": ["/dev/sdb", "/dev/sdc"]}).
func PostRAIDArray(ctx echo.Context) error {
	var request struct {
		Name    string   `json:"name"`
		Level   string   `json:"level"`
		Disks   []string `json:"disks"`
		Confirm string   `json:"confirm"`
		// AcceptDataLoss is what a RAID 0 asks for in addition: it has no protection
		AcceptDataLoss bool `json:"accept_data_loss"`
	}
	if err := ctx.Bind(&request); err != nil || request.Name == "" || request.Confirm != request.Name {
		return ctx.JSON(http.StatusBadRequest, model.Result{Success: common_err.INVALID_PARAMS, Message: "the name of the storage was not typed again", Data: "the name of the storage was not typed again"})
	}
	if len(request.Name) > storageLabelMaxLength || !storageLabelPattern.MatchString(request.Name) {
		return ctx.JSON(http.StatusBadRequest, model.Result{Success: common_err.INVALID_PARAMS, Message: "a storage name is letters, digits and underscores, 16 at most", Data: "a storage name is letters, digits and underscores, 16 at most"})
	}
	if request.Level == "raid0" && !request.AcceptDataLoss {
		return raidAnswer(ctx, service.ErrRAIDConsent)
	}
	for _, path := range request.Disks {
		if _, ok := diskMap[path]; ok {
			return ctx.JSON(common_err.SERVICE_ERROR, model.Result{Success: common_err.DISK_BUSYING, Message: common_err.GetMsg(common_err.DISK_BUSYING)})
		}
	}
	for _, path := range request.Disks {
		diskMap[path] = "busying"
	}
	defer func() {
		for _, path := range request.Disks {
			delete(diskMap, path)
		}
	}()
	defer service.MyService.Disk().RemoveLSBLKCache()

	blk := service.MyService.Disk().LSBLK(false)
	disks := make([]model1.LSBLKModel, 0, len(request.Disks))
	for _, path := range request.Disks {
		disk, err := service.RAIDCandidate(blk, path, 0)
		if err != nil {
			return raidAnswer(ctx, err)
		}
		disks = append(disks, disk)
	}
	arrayPath, err := service.CreateRAIDArray(service.RAIDArrays("/sys"), request.Level, disks)
	if err != nil {
		logger.Error("error when making a RAID array", zap.Error(err), zap.String("level", request.Level), zap.Strings("disks", request.Disks))
		return raidAnswer(ctx, err)
	}
	// from here the disks are erased and the array exists: say which step failed, for the person to finish by hand
	if err := service.MyService.Disk().FormatDisk(arrayPath); err != nil {
		logger.Error("error when formatting a new RAID array", zap.Error(err), zap.String("array", arrayPath))
		return raidAnswer(ctx, fmt.Errorf("%s is made but could not be formatted: %w", arrayPath, err))
	}
	if !mountStorage(service.MyService.Disk().GetDiskInfo(arrayPath), request.Name) {
		return raidAnswer(ctx, fmt.Errorf("%s is made and formatted but could not be mounted", arrayPath))
	}
	array, _ := service.RAIDArrayNamed("/sys", strings.TrimPrefix(arrayPath, "/dev/"))
	return ctx.JSON(common_err.SUCCESS, model.Result{Success: common_err.SUCCESS, Message: common_err.GetMsg(common_err.SUCCESS), Data: array})
}

// DeleteRAIDDisk takes a failed disk (its member name, sdc1) out of an array.
func DeleteRAIDDisk(ctx echo.Context) error {
	array, err := service.RAIDArrayNamed("/sys", ctx.Param("name"))
	if err != nil {
		return raidAnswer(ctx, err)
	}
	if err := service.RemoveRAIDDisk(array, ctx.Param("member")); err != nil {
		logger.Error("error when taking a disk out of a RAID array", zap.Error(err), zap.String("array", array.Path), zap.String("member", ctx.Param("member")))
		return raidAnswer(ctx, err)
	}
	defer service.MyService.Disk().RemoveLSBLKCache()
	array, _ = service.RAIDArrayNamed("/sys", array.Name)
	return ctx.JSON(common_err.SUCCESS, model.Result{Success: common_err.SUCCESS, Message: common_err.GetMsg(common_err.SUCCESS), Data: array})
}

func GetDiskSize(ctx echo.Context) error {
	path := ctx.QueryParam("path")
	if len(path) == 0 {
		return ctx.JSON(common_err.CLIENT_ERROR, model.Result{Success: common_err.INVALID_PARAMS, Message: common_err.GetMsg(common_err.INVALID_PARAMS)})
	}
	p, err := disk.Usage(path)
	if err != nil {
		return ctx.JSON(common_err.SERVICE_ERROR, model.Result{Success: common_err.SERVICE_ERROR, Message: err.Error()})
	}
	data := map[string]interface{}{
		"path": path,
		"free": p.Free,
		"used": p.Used,
	}
	return ctx.JSON(common_err.SUCCESS, model.Result{Success: common_err.SUCCESS, Message: common_err.GetMsg(common_err.SUCCESS), Data: data})
}
