package v2

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/ReCasaOS/CasaOS-Common/utils"
	"github.com/ReCasaOS/CasaOS-Common/utils/file"
	"github.com/ReCasaOS/CasaOS-Common/utils/logger"
	"github.com/ReCasaOS/CasaOS-LocalStorage/codegen"
	"github.com/ReCasaOS/CasaOS-LocalStorage/common"
	"github.com/ReCasaOS/CasaOS-LocalStorage/pkg/mergerfs"
	"github.com/ReCasaOS/CasaOS-LocalStorage/pkg/partition"
	"github.com/ReCasaOS/CasaOS-LocalStorage/pkg/utils/command"
	model2 "github.com/ReCasaOS/CasaOS-LocalStorage/service/model"
	"github.com/tidwall/gjson"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

var (
	ErrMergeMountPointAlreadyExists  = errors.New("merge mount point already exists")
	ErrMergeMountPointDoesNotExist   = errors.New("merge mount point does not exist")
	ErrMergeMountPointSourceConflict = errors.New("source mount point should not be a child path of the merge mount point")
	ErrMergeHasNoSources             = errors.New("a merge must have at least one source")
	ErrNilReference                  = errors.New("reference is nil")
)

// Make sure the serial disk is removed from the merge list when it is deleted from database, to keep the database consistent.
func hookAfterDeleteVolume(db *gorm.DB, model interface{}) {
	var targetVolumes []model2.Volume

	switch t := model.(type) {
	case model2.Volume:
		targetVolumes = []model2.Volume{t}
	case *model2.Volume:
		targetVolumes = []model2.Volume{*t}
	case []model2.Volume:
		targetVolumes = t
	case *[]model2.Volume:
		targetVolumes = *t
	default:
		return
	}

	var merges []model2.Merge

	if err := db.Model(&model2.Merge{}).Preload(model2.MergeSourceVolumes).Find(&merges).Error; err != nil {
		logger.Error("failed to get merge list from database", zap.Error(err))
		return
	}

	for i := range merges {
		updatedVolumes := make([]*model2.Volume, 0)
		for _, sourceVolume := range merges[i].SourceVolumes {
			for _, targetVolume := range targetVolumes {
				if sourceVolume.ID == targetVolume.ID {
					break // skip including the volume to be deleted
				}
				updatedVolumes = append(updatedVolumes, sourceVolume)
			}
		}

		if err := db.Model(&merges[i]).Association(model2.MergeSourceVolumes).Error; err != nil {
			logger.Error("failed to enter association mode between merges and volumes", zap.Error(err), zap.Any("merge", merges[i]))
			return
		}

		if err := db.Model(&merges[i]).Association(model2.MergeSourceVolumes).Replace(updatedVolumes); err != nil {
			logger.Error("failed to update merge source volumes", zap.Error(err), zap.Any("merge", merges[i]), zap.Any("updatedVolumes", updatedVolumes))
			return
		}
	}
}

func (s *LocalStorageService) GetMerges(mountPoint *string) ([]model2.Merge, error) {
	mergesFromDB, err := s.GetMergeAllFromDB(mountPoint)
	if err != nil {
		return nil, err
	}
	// Keep configured-but-currently-missing volumes in the response so the UI
	// can warn about them and preserve them for automatic reattachment. Runtime
	// mount operations filter unavailable volumes below.
	return mergesFromDB, nil
}

func (s *LocalStorageService) CreateMerge(merge *model2.Merge) error {
	s._mergeMu.Lock()
	defer s._mergeMu.Unlock()
	return s.createMergeLocked(merge)
}

func (s *LocalStorageService) createMergeLocked(merge *model2.Merge) error {
	if merge == nil {
		logger.Error("`merge` should not be nil")
		return ErrNilReference
	}

	if err := file.IsNotExistMkDir(merge.MountPoint); err != nil {
		return err
	}

	mountedSourceVolumes := excludeVolumesWithWrongMountPointAndUUID(merge.SourceVolumes)
	mountMerge := *merge
	mountMerge.SourceVolumes = mountedSourceVolumes

	sources, err := buildSources(&mountMerge)
	if err != nil {
		logger.Error("failed to build sources", zap.Error(err))
		return err
	}
	if len(sources) == 0 {
		return ErrMergeHasNoSources
	}

	// check if the mount point is empty before creating a new mergerfs mount
	if bool, err := file.IsDirEmpty(merge.MountPoint); err != nil {
		logger.Error("failed to check if the mount point is empty", zap.Error(err))
		return err
	} else if !bool {
		contents := mountPointContents(merge.MountPoint)
		logger.Error("mount point is not empty", zap.String("mountPoint", merge.MountPoint), zap.String("contents", contents))
		return fmt.Errorf("%w: %s", ErrMountPointIsNotEmpty, contents)
	}

	// create a new merge by mounting mergerfs
	source := strings.Join(sources, ":")
	if _, err := s.Mount(codegen.Mount{
		MountPoint: merge.MountPoint,
		Fstype:     &merge.FSType,
		Source:     &source,
	}); err != nil {
		logger.Error("failed to mount mergerfs", zap.Error(err), zap.String("mountPoint", merge.MountPoint), zap.String("source", source))
		return err
	}
	if err := s.ensureAppDataCompatibilityMount(merge); err != nil {
		logger.Error("failed to expose system AppData at /DATA/AppData", zap.Error(err), zap.String("mountPoint", merge.MountPoint))
		if unmountErr := s.Umount(merge.MountPoint); unmountErr != nil && !errors.Is(unmountErr, ErrNotMounted) {
			logger.Error("failed to roll back mergerfs mount after AppData setup failure", zap.Error(unmountErr), zap.String("mountPoint", merge.MountPoint))
		}
		return err
	}
	if merge.MountPoint == common.DefaultMountPoint {
		if err := EnsureDefaultDirectories(merge.MountPoint); err != nil {
			logger.Error("failed to create default data directories", zap.Error(err), zap.String("mountPoint", merge.MountPoint))
			if usesExternalDataBranches(merge) {
				if unmountErr := s.unmountAppDataCompatibilityMount(); unmountErr != nil {
					logger.Error("failed to roll back AppData compatibility mount", zap.Error(unmountErr), zap.String("mountPoint", merge.MountPoint))
				}
			}
			if unmountErr := s.Umount(merge.MountPoint); unmountErr != nil && !errors.Is(unmountErr, ErrNotMounted) {
				logger.Error("failed to roll back mergerfs mount after default directory setup failure", zap.Error(unmountErr), zap.String("mountPoint", merge.MountPoint))
			}
			return err
		}
	}

	return nil
}

func (s *LocalStorageService) UpdateMerge(merge *model2.Merge) error {
	s._mergeMu.Lock()
	defer s._mergeMu.Unlock()
	return s.updateMergeLocked(merge)
}

func (s *LocalStorageService) updateMergeLocked(merge *model2.Merge) error {
	if merge == nil {
		logger.Error("`merge` should not be nil")
		return ErrNilReference
	}

	if !file.Exists(merge.MountPoint) {
		return ErrMergeMountPointDoesNotExist
	}

	mountedSourceVolumes := excludeVolumesWithWrongMountPointAndUUID(merge.SourceVolumes)
	mountMerge := *merge
	mountMerge.SourceVolumes = mountedSourceVolumes

	sources, err := buildSources(&mountMerge)
	if err != nil {
		logger.Error("failed to build sources", zap.Error(err))
		return err
	}
	if len(sources) == 0 {
		return ErrMergeHasNoSources
	}

	// if it is already a merge point, check if the mount point is a mergerfs mount with the same sources
	existingSources, err := mergerfs.GetSource(merge.MountPoint)
	if err != nil {
		logger.Error("failed to get mergerfs sources", zap.Error(err), zap.String("mountPoint", merge.MountPoint))
		return err
	}

	sourcesChanged := !utils.CompareStringSlices(sources, existingSources)
	if sourcesChanged {
		// update the mergerfs sources if different sources
		if err := mergerfs.SetSource(merge.MountPoint, sources); err != nil {
			logger.Error("failed to set mergerfs sources", zap.Error(err), zap.String("mountPoint", merge.MountPoint), zap.Any("sources", sources))
			return err
		}
	}
	if err := s.ensureAppDataCompatibilityMount(merge); err != nil {
		logger.Error("failed to expose system AppData at /DATA/AppData", zap.Error(err), zap.String("mountPoint", merge.MountPoint))
		if sourcesChanged {
			if rollbackErr := mergerfs.SetSource(merge.MountPoint, existingSources); rollbackErr != nil {
				logger.Error("failed to roll back mergerfs sources after AppData setup failure", zap.Error(rollbackErr), zap.String("mountPoint", merge.MountPoint))
			}
		}
		return err
	}
	if merge.MountPoint == common.DefaultMountPoint {
		if err := EnsureDefaultDirectories(merge.MountPoint); err != nil {
			logger.Error("failed to create default data directories", zap.Error(err), zap.String("mountPoint", merge.MountPoint))
			if sourcesChanged {
				if rollbackErr := mergerfs.SetSource(merge.MountPoint, existingSources); rollbackErr != nil {
					logger.Error("failed to roll back mergerfs sources after default directory setup failure", zap.Error(rollbackErr), zap.String("mountPoint", merge.MountPoint))
				}
			}
			return err
		}
	}

	return nil
}

// CheckMergeMount restores every merge from the database that is not currently
// mounted. It is safe to call repeatedly (the periodic reconciler in main
// calls it every 30 seconds) and it is a no-op while all merges are mounted.
func (s *LocalStorageService) CheckMergeMount() {
	s._mergeMu.Lock()
	defer s._mergeMu.Unlock()

	mergesFromDB, err := s.GetMergeAllFromDB(nil)
	if err != nil {
		logger.Error("failed to get merge list from database", zap.Error(err))
		return
	}

	mounts, err := s.GetMounts(codegen.GetMountsParams{})
	if err != nil {
		logger.Error("failed to get mount list from system", zap.Error(err))
		return
	}

	for i := range mergesFromDB {
		merge := &mergesFromDB[i]

		isMergeExist := false

		// for each merge from database by mount point, check if it already mounted, i.e. a mergerfs mount
		for _, mount := range mounts {
			if mount.MountPoint == merge.MountPoint {
				if *mount.Fstype == merge.FSType {
					isMergeExist = true
					break
				}
				logger.Error("a non-mergerfs mount occupies the merge mount point", zap.Any("mount", mount))
			}
		}

		if isMergeExist {
			if err := s.updateMergeLocked(merge); err != nil {
				logger.Error("failed to update merge", zap.Error(err), zap.Any("merge", *merge))
			} else {
				s.clearMergeErrorLocked(merge.MountPoint)
			}
			continue
		}

		// the merge may have been removed from the database after the list was read
		freshMerge, err := s.GetFirstMergeFromDB(merge.MountPoint)
		if err != nil {
			logger.Error("failed to re-check merge in database", zap.Error(err), zap.Any("merge", *merge))
			continue
		}
		if freshMerge == nil {
			logger.Info("merge was removed, skipping restore", zap.String("mountPoint", merge.MountPoint))
			continue
		}

		err = s.createMergeLocked(merge)
		if errors.Is(err, ErrMountPointIsNotEmpty) {
			// only here, for a merge the box already has: a merge someone asks
			// for through the API still refuses a mount point that is not empty
			setAside, setAsideErr := s.setAsideForRestoreLocked(merge.MountPoint)
			if setAside != "" {
				logger.Error("the merge mount point was not empty: its contents were moved, not deleted", zap.String("mountPoint", merge.MountPoint), zap.String("movedTo", setAside))
			}
			if setAsideErr != nil {
				logger.Error("failed to set aside the contents of the merge mount point", zap.Error(setAsideErr), zap.String("mountPoint", merge.MountPoint))
			} else {
				err = s.createMergeLocked(merge)
			}
		}
		if err != nil {
			logger.Error("failed to restore merge", zap.Error(err), zap.Any("merge", *merge))
			s.recordMergeErrorLocked(merge.MountPoint, err)
		} else {
			logger.Info("restored merge mount", zap.String("mountPoint", merge.MountPoint))
			s.clearMergeErrorLocked(merge.MountPoint)
		}
	}
}

func (s *LocalStorageService) recordMergeErrorLocked(mountPoint string, err error) {
	s._mergeErrors[mountPoint] = err.Error()
}

func (s *LocalStorageService) clearMergeErrorLocked(mountPoint string) {
	delete(s._mergeErrors, mountPoint)
}

// LastMergeRestoreError returns the most recent restore failure for the given
// merge mount point, or an empty string when the merge is healthy.
func (s *LocalStorageService) LastMergeRestoreError(mountPoint string) string {
	s._mergeMu.Lock()
	defer s._mergeMu.Unlock()
	return s._mergeErrors[mountPoint]
}

// IsMergeMounted reports whether the given mount point is currently a mergerfs
// mount of the given fstype.
func (s *LocalStorageService) IsMergeMounted(mountPoint string, fstype string) bool {
	s._mergeMu.Lock()
	defer s._mergeMu.Unlock()

	mounts, err := s.GetMounts(codegen.GetMountsParams{MountPoint: &mountPoint})
	if err != nil {
		return false
	}
	for i := range mounts {
		if mounts[i].Fstype != nil && *mounts[i].Fstype == fstype {
			return true
		}
	}
	return false
}

// AreAllMergesMounted reports whether every merge in the database is
// currently a live mergerfs mount.
func (s *LocalStorageService) AreAllMergesMounted() bool {
	s._mergeMu.Lock()
	defer s._mergeMu.Unlock()

	mergesFromDB, err := s.GetMergeAllFromDB(nil)
	if err != nil {
		logger.Error("failed to get merge list from database", zap.Error(err))
		return false
	}

	mounts, err := s.GetMounts(codegen.GetMountsParams{})
	if err != nil {
		logger.Error("failed to get mount list from system", zap.Error(err))
		return false
	}

	for i := range mergesFromDB {
		mounted := false
		for _, mount := range mounts {
			if mount.MountPoint == mergesFromDB[i].MountPoint &&
				mount.Fstype != nil && *mount.Fstype == mergesFromDB[i].FSType {
				mounted = true
				break
			}
		}
		if !mounted {
			return false
		}
	}

	return true
}

// WaitForMerges keeps invoking restore until allMounted reports success or
// attempts is exhausted, sleeping interval between attempts. It returns true
// when every merge is mounted.
func WaitForMerges(restore func(), allMounted func() bool, attempts int, interval time.Duration) bool {
	for i := 0; i < attempts; i++ {
		if allMounted() {
			return true
		}
		restore()
		if allMounted() {
			return true
		}
		time.Sleep(interval)
	}
	return allMounted()
}

func mountPointContents(mountPoint string) string {
	entries, err := os.ReadDir(mountPoint)
	if err != nil {
		return "could not list mount point contents"
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	if len(names) == 0 {
		return "mount point reports as not empty but lists no entries"
	}
	return "contains: " + strings.Join(names, ", ")
}

// filter out any volume that are not mounted based on its UUID and mount point (in reality, could have a different disk mounted on the same path)
func excludeVolumesWithWrongMountPointAndUUID(volumes []*model2.Volume) []*model2.Volume {
	return filterVolumes(volumes, func(v *model2.Volume) bool {
		path, err := partition.GetDevicePath(v.UUID)
		if err != nil {
			logger.Error("failed to corresponding device path by volume UUID", zap.Error(err), zap.String("uuid", v.UUID))
			return false
		}

		par := command.ExecLSBLKByPath(path)
		pttype := gjson.GetBytes(par, "blockdevices.0.pttype")
		if pttype.String() != "gpt" {
			mountPoint := gjson.GetBytes(par, "blockdevices.0.mountpoint")
			if mountPoint.String() != v.MountPoint {
				logger.Error("mount point does not match actual", zap.Any("volume", v), zap.String("actual mount point", mountPoint.String()))
				return false
			}
			return true

		}

		partitions, err := partition.GetPartitions(path)
		if err != nil {
			logger.Error("failed to corresponding partition of volume", zap.Error(err), zap.String("path", path))
			return false
		}

		if len(partitions) != 1 {
			logger.Error("there should be exactly one partition corresponding to the volume", zap.String("path", path), zap.Int("partitions", len(partitions)))
			return false
		}

		if partitions[0].LSBLKProperties["MOUNTPOINT"] != v.MountPoint {
			logger.Error("mount point does not match actual", zap.Any("volume", v), zap.String("actual mount point", partitions[0].LSBLKProperties["MOUNTPOINT"]))
			return false
		}

		return true
	})
}

func filterVolumes(volumes []*model2.Volume, filter func(*model2.Volume) bool) []*model2.Volume {
	var filteredVolumes []*model2.Volume
	for _, volume := range volumes {
		result := filter(volume)
		if result {
			filteredVolumes = append(filteredVolumes, volume)
		}
	}
	return filteredVolumes
}

func buildSources(merge *model2.Merge) ([]string, error) {
	sources := make([]string, 0)

	if merge.SourceBasePath != nil && *merge.SourceBasePath != "" {
		// check if sourceBasePath is under mount point
		if strings.HasPrefix(*merge.SourceBasePath, merge.MountPoint) {
			logger.Error(
				"source base path should not be a child path of the merge mount point",
				zap.String("sourceBasePath", *merge.SourceBasePath),
				zap.String("merge.MountPoint", merge.MountPoint),
			)
			return nil, ErrMergeMountPointSourceConflict
		}

		// create source path if it does not exists
		if err := file.IsNotExistMkDir(*merge.SourceBasePath); err != nil {
			return nil, err
		}

		sources = append(sources, *merge.SourceBasePath)
	}

	for _, sourceVolume := range merge.SourceVolumes {
		if sourceVolume == nil {
			logger.Error("one of the source volumes is nil", zap.Any("sourceVolumes", merge.SourceVolumes))
			return nil, ErrNilReference
		}

		// check if sourceBasePath is under mount point
		if strings.HasPrefix(sourceVolume.MountPoint, merge.MountPoint) {
			logger.Error(
				"mount point of source volume should not be a child path of the mount point",
				zap.Any("sourceVolume.MountPoint", sourceVolume.MountPoint),
				zap.Any("merge.MountPoint", merge.MountPoint),
			)
			return nil, ErrMergeMountPointSourceConflict
		}

		sources = append(sources, sourceVolume.MountPoint)
	}

	return sources, nil
}
