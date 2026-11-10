//go:build linux

package model

import (
	"path/filepath"
	"testing"
)

// A mount point is one folder of /media, whatever the name asked for and whatever
// the disk says its label and its model are.
func TestAMountPointIsOneFolderOfMedia(t *testing.T) {
	for _, c := range []struct {
		name string
		disk LSBLKModel
	}{
		{"", LSBLKModel{Name: "sdb1", Label: "x/../../../etc"}},
		{"", LSBLKModel{Name: "sdb1", Model: "../../root"}},
		{"../../etc", LSBLKModel{Name: "sdb1"}},
		{"..", LSBLKModel{Name: "sdb1"}},
		{".", LSBLKModel{Name: "sdb1"}},
		{"My Disk", LSBLKModel{Name: "sdb1", Label: "My Passport", Model: "WD Elements"}},
	} {
		got := c.disk.GetMountPoint(c.name)
		if filepath.Dir(got) != defaultMountPath || got == defaultMountPath {
			t.Errorf("%q on %+v is mounted at %q, want a folder of %s", c.name, c.disk, got, defaultMountPath)
		}
	}

	// a name with nothing wrong in it is kept as it is
	photos := LSBLKModel{Name: "sdb1", Label: "Photos 2024"}
	if got := photos.GetMountPoint(""); got != "/media/Storage_sdb1_Photos 2024" {
		t.Errorf("got %q", got)
	}
}
