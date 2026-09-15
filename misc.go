package main

import (
	"context"
	"net/http"
	"reflect"
	"strings"
	"time"

	"github.com/ReCasaOS/CasaOS-Common/utils/logger"
	"github.com/ReCasaOS/CasaOS-LocalStorage/common"
	"github.com/ReCasaOS/CasaOS-LocalStorage/model"
	"github.com/ReCasaOS/CasaOS-LocalStorage/service"
	"github.com/pilebones/go-udev/netlink"
	"go.uber.org/zap"
)

func sendDiskBySocket() {
	blkList := service.MyService.Disk().LSBLK(true)

	status := model.DiskStatus{}
	healths := []string{}
	systemDiskFound := false

	for _, currentDisk := range blkList {
		if !service.IsDiskSupported(currentDisk) {
			continue
		}
		healths = append(healths, service.MyService.Disk().SmartCTL(currentDisk.Path).SmartHealth())

		if !systemDiskFound {
			if systemStats, ok := service.MountedFilesystemStatsAt(currentDisk, "/"); ok {
				status.Size = systemStats.Size
				status.Avail = systemStats.Avail
				status.Used = systemStats.Used
				systemDiskFound = true
			}
		}
	}

	// Keep a useful fallback for systems where lsblk cannot identify "/": the
	// space of every mounted filesystem on a supported disk, each counted once,
	// since lsblk lists a RAID array again under every disk it is built on.
	if !systemDiskFound {
		supported := make([]model.LSBLKModel, 0, len(blkList))
		for _, currentDisk := range blkList {
			if service.IsDiskSupported(currentDisk) {
				supported = append(supported, currentDisk)
			}
		}
		stats := service.StorageUsage(supported)
		status.Size, status.Avail, status.Used = stats.Size, stats.Avail, stats.Used
	}

	status.SmartStatus = model.AggregateSmartHealth(healths...)
	status.Health = status.SmartStatus != model.SmartHealthFailed
	message := make(map[string]interface{})
	message["sys_disk"] = status
	if err := service.MyService.NotifySystem().SendSystemStatusNotify(message); err != nil {
		logger.Error("failed to send notify", zap.Any("message", message), zap.Error(err))
	}
}

func sendUSBBySocket() {
	message := map[string]interface{}{
		"sys_usb": service.MyService.Disk().GetUSBDriveStatusList(),
	}

	if err := service.MyService.NotifySystem().SendSystemStatusNotify(message); err != nil {
		logger.Error("failed to send notify", zap.Any("message", message), zap.Error(err))
	}
}

func monitorUEvent(ctx context.Context) {
	var matcher netlink.Matcher

	conn := new(netlink.UEventConn)
	if err := conn.Connect(netlink.UdevEvent); err != nil {
		logger.Error("udev err", zap.Any("Unable to connect to Netlink Kobject UEvent socket", err))
	}
	defer conn.Close()

	queue := make(chan netlink.UEvent)
	defer close(queue)

	errors := make(chan error)
	defer close(errors)

	quit := conn.Monitor(queue, errors, matcher)
	defer close(quit)

	for {
		select {

		case <-ctx.Done():
			return

		case uevent := <-queue:

			if event := common.EventAdapter(uevent); event != nil {

				// add UI properties to applicable events so that CasaOS UI can render it
				event := common.EventAdapterWithUIProperties(event)

				if v, ok := event.Properties["local-storage:path"]; ok && strings.Contains(event.Name, "disk") {

					diskModel := service.MyService.Disk().GetDiskInfo(v)
					if !reflect.DeepEqual(diskModel, model.LSBLKModel{}) {

						properties := common.AdditionalProperties(diskModel)
						for k, v := range properties {
							event.Properties[k] = v
						}
					}
				}
				logger.Info("disk model", zap.Any("diskModel", event.Name))
				response, err := service.MyService.MessageBus().PublishEventWithResponse(ctx, event.SourceID, event.Name, event.Properties)
				if err != nil {
					logger.Error("failed to publish event to message bus", zap.Error(err), zap.Any("event", event))
				}

				if response.StatusCode() != http.StatusOK {
					logger.Error("failed to publish event to message bus", zap.String("status", response.Status()), zap.Any("response", response))
				}
			}

			switch uevent.Env["DEVTYPE"] {
			case "partition":

				switch uevent.Env["ID_BUS"] {
				case "usb":
					time.Sleep(1 * time.Second)
					sendUSBBySocket()
					continue
				}
			}

		case err := <-errors:
			logger.Error("udev err", zap.Error(err))
		}
	}
}

func sendStorageStats() {
	sendDiskBySocket()
	sendUSBBySocket()
}
