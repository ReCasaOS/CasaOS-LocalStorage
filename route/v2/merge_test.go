package v2

import (
	"testing"

	"github.com/ReCasaOS/CasaOS-LocalStorage/codegen"
	"github.com/ReCasaOS/CasaOS-LocalStorage/common"
	model2 "github.com/ReCasaOS/CasaOS-LocalStorage/service/model"
)

func TestApplyMergeSourcesClearsBootstrapPathForExplicitVolumes(t *testing.T) {
	basePath := "/var/lib/casaos/files"
	volumeUUIDs := []string{"data-volume"}
	volumes := []*model2.Volume{{UUID: "data-volume", MountPoint: "/mnt/data"}}
	merge := &model2.Merge{SourceBasePath: &basePath}

	applyMergeSources(merge, nil, &volumeUUIDs, volumes)

	if merge.SourceBasePath != nil {
		t.Fatalf("expected bootstrap source path to be cleared, got %q", *merge.SourceBasePath)
	}
	if len(merge.SourceVolumes) != 1 || merge.SourceVolumes[0] != volumes[0] {
		t.Fatalf("expected explicit source volumes to be applied, got %#v", merge.SourceVolumes)
	}
}

func TestApplyMergeSourcesKeepsExplicitBasePath(t *testing.T) {
	basePath := "/mnt/system-data"
	volumeUUIDs := []string{"data-volume"}
	volumes := []*model2.Volume{{UUID: "data-volume", MountPoint: "/mnt/data"}}
	merge := &model2.Merge{}

	applyMergeSources(merge, &basePath, &volumeUUIDs, volumes)

	if merge.SourceBasePath == nil || *merge.SourceBasePath != basePath {
		t.Fatalf("expected explicit base path %q, got %#v", basePath, merge.SourceBasePath)
	}
}

func TestApplyMergeSourcesNormalizesEmptyBasePath(t *testing.T) {
	basePath := ""
	volumeUUIDs := []string{"data-volume"}
	volumes := []*model2.Volume{{UUID: "data-volume", MountPoint: "/mnt/data"}}
	merge := &model2.Merge{}

	applyMergeSources(merge, &basePath, &volumeUUIDs, volumes)

	if merge.SourceBasePath != nil {
		t.Fatalf("expected an empty base path to be normalized away, got %q", *merge.SourceBasePath)
	}
}

func TestIsFullMergeRemovalRequiresExplicitEmptySelection(t *testing.T) {
	emptySources := []string{}
	basePath := ""
	if !isFullMergeRemoval(codegen.Merge{
		MountPoint:        common.DefaultMountPoint,
		SourceBasePath:    &basePath,
		SourceVolumeUuids: &emptySources,
	}) {
		t.Fatal("expected an explicit empty source selection to request a full unmerge")
	}

	if isFullMergeRemoval(codegen.Merge{MountPoint: common.DefaultMountPoint}) {
		t.Fatal("expected an omitted source selection not to request a full unmerge")
	}
}

func TestIsFullMergeRemovalAppliesToAnyMountPoint(t *testing.T) {
	emptySources := []string{}
	if !isFullMergeRemoval(codegen.Merge{MountPoint: "/media/Pool", SourceVolumeUuids: &emptySources}) {
		t.Fatal("expected an explicit empty selection to unmerge a merge that is not on /DATA")
	}

	if isFullMergeRemoval(codegen.Merge{SourceVolumeUuids: &emptySources}) {
		t.Fatal("expected a request without a mount point not to be a full unmerge")
	}
}
