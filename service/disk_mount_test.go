package service

import (
	"testing"

	"github.com/ReCasaOS/CasaOS-LocalStorage/pkg/partition"
)

func TestTheMountPointOfADeviceWithoutPartitionsIsItsOwn(t *testing.T) {
	asked := 0
	device := func() string { asked++; return "/media/Fast" }

	if got := actualMountPoint(nil, device); got != "/media/Fast" || asked != 1 {
		t.Errorf("an array with no partition: %q after %d question(s), want /media/Fast after 1", got, asked)
	}
	if got := actualMountPoint([]partition.Partition{}, device); got != "/media/Fast" {
		t.Errorf("an empty list: %q, want /media/Fast", got)
	}

	asked = 0
	partitions := []partition.Partition{{LSBLKProperties: map[string]string{"MOUNTPOINT": "/media/sdb1"}}}
	if got := actualMountPoint(partitions, device); got != "/media/sdb1" || asked != 0 {
		t.Errorf("a partitioned disk: %q after %d question(s), want /media/sdb1 after none", got, asked)
	}
}
