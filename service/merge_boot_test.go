package service

import (
	"testing"

	"github.com/ReCasaOS/CasaOS-Common/utils/logger"
	"github.com/ReCasaOS/CasaOS-LocalStorage/pkg/config"
	"github.com/ReCasaOS/CasaOS-LocalStorage/pkg/sqlite"
	model2 "github.com/ReCasaOS/CasaOS-LocalStorage/service/model"
	v2 "github.com/ReCasaOS/CasaOS-LocalStorage/service/v2"
)

// a box whose only merge is not on /DATA has to mount it again at boot
func TestAMergeOutsideDataKeepsMergerFSEnabled(t *testing.T) {
	logger.LogInitConsoleOnly()
	db := sqlite.GetDBByFile("file:mergeboot?mode=memory&cache=shared")

	previousService, previousFlag := MyService, config.ServerInfo.EnableMergerFS
	defer func() { MyService, config.ServerInfo.EnableMergerFS = previousService, previousFlag }()
	disk := &diskService{db: db}
	MyService = &store{disk: disk, localStorage: v2.NewLocalStorageService(db, nil)}

	config.ServerInfo.EnableMergerFS = "true"
	if !disk.EnsureDefaultMergePoint() || config.ServerInfo.EnableMergerFS != "false" {
		t.Fatalf("expected mergerfs to be disabled while nothing is merged, got %q", config.ServerInfo.EnableMergerFS)
	}

	if err := db.Create(&model2.Merge{FSType: "fuse.mergerfs", MountPoint: "/media/Pool"}).Error; err != nil {
		t.Fatal(err)
	}
	if !disk.EnsureDefaultMergePoint() || config.ServerInfo.EnableMergerFS != "true" {
		t.Fatalf("expected the merge on /media/Pool to keep mergerfs enabled, got %q", config.ServerInfo.EnableMergerFS)
	}
}
