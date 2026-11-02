package service

import (
	"errors"
	"reflect"
	"testing"

	"github.com/ReCasaOS/CasaOS-LocalStorage/model"
)

func threeDiskArray() RAIDArray {
	return RAIDArray{Name: "md0", Path: "/dev/md0", Level: "raid5", State: "ok", Disks: 3,
		Members: []RAIDMember{{Name: "sdb", Disk: "sdb", State: "active"}, {Name: "sdc", Disk: "sdc", State: "active"}, {Name: "sdd1", Disk: "sdd", State: "active"}}}
}

// A whole disk is wiped as well; a partition only loses its RAID header, and the rest of its disk is left alone.
func TestAnArrayIsStoppedThenItsMembersAreErased(t *testing.T) {
	ran := recordRuns(t, true, "")
	if err := DeleteRAIDArray(threeDiskArray()); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"mdadm --stop /dev/md0",
		"mdadm --zero-superblock /dev/sdb", "wipefs --all /dev/sdb",
		"mdadm --zero-superblock /dev/sdc", "wipefs --all /dev/sdc",
		"mdadm --zero-superblock /dev/sdd1",
	}
	if !reflect.DeepEqual(*ran, want) {
		t.Fatalf("ran %q, want %q", *ran, want)
	}
}

func TestAnArrayThatWillNotStopIsLeftAsItWas(t *testing.T) {
	ran := recordRuns(t, true, "mdadm --stop")
	if err := DeleteRAIDArray(threeDiskArray()); !errors.Is(err, ErrRAIDInUse) {
		t.Fatalf("error %v, want ErrRAIDInUse", err)
	}
	if len(*ran) != raidStopTries {
		t.Fatalf("ran %q, want %d tries to stop and nothing else", *ran, raidStopTries)
	}
	for _, line := range *ran {
		if line != "mdadm --stop /dev/md0" {
			t.Errorf("ran %q after a stop that failed", line)
		}
	}
}

// A drive that failed and was pulled has no device to erase: that is no reason to keep the others' headers.
func TestAFailedMemberThatIsGoneDoesNotStopTheDeletion(t *testing.T) {
	array := threeDiskArray()
	array.State = "degraded"
	array.Members[1].State = "faulty"
	ran := recordRuns(t, true, "mdadm --zero-superblock /dev/sdc")
	if err := DeleteRAIDArray(array); err != nil {
		t.Fatal(err)
	}
	if last := (*ran)[len(*ran)-1]; last != "mdadm --zero-superblock /dev/sdd1" {
		t.Errorf("the members after the missing one were not erased: ran %q", *ran)
	}

	array.Members[1].State = "active"
	recordRuns(t, true, "mdadm --zero-superblock /dev/sdc")
	if err := DeleteRAIDArray(array); err == nil {
		t.Error("an active member that cannot be erased was let go")
	}
}

func TestOnlyAnArrayNothingNeedsMayBeDeleted(t *testing.T) {
	mounted := func(uuid, mount string, children ...model.LSBLKModel) model.LSBLKModel {
		return model.LSBLKModel{Name: "md0", Path: "/dev/md0", Type: "raid5", UUID: uuid, MountPoint: mount, Children: children}
	}
	taken := []string{"/media/pool1", "uuid-pool"}
	for name, c := range map[string]struct {
		blk  model.LSBLKModel
		want error
	}{
		"a storage of its own":                 {mounted("u1", "/media/test"), nil},
		"an array nothing mounts":              {mounted("", ""), nil},
		"the system":                           {mounted("u1", "/"), ErrRAIDSystem},
		"a partition of the system":            {mounted("", "", model.LSBLKModel{Name: "md0p1", MountPoint: "/"}), ErrRAIDSystem},
		"a disk of the merged storage":         {mounted("u1", "/media/pool1"), ErrRAIDPooled},
		"a volume the merged storage knows":    {mounted("uuid-pool", ""), ErrRAIDPooled},
		"a partition of the merged storage":    {mounted("", "", model.LSBLKModel{Name: "md0p1", MountPoint: "/media/pool1"}), ErrRAIDPooled},
		"a storage on a partition":             {mounted("", "", model.LSBLKModel{Name: "md0p1", MountPoint: "/media/test"}), nil},
		"a volume stacked on a partition":      {mounted("", "", model.LSBLKModel{Name: "md0p1", Children: []model.LSBLKModel{{Name: "vg-lv", MountPoint: "/media/lv"}}}), ErrRAIDLayered},
		"a volume stacked on the array itself": {mounted("", "", model.LSBLKModel{Name: "vg-lv", MountPoint: "/media/lv"}), nil}, // a direct child is a volume, which RAIDVolumes takes down
		"a stacked volume that nothing mounts": {mounted("", "", model.LSBLKModel{Name: "md0p1", Children: []model.LSBLKModel{{Name: "vg-lv"}}}), nil},
	} {
		if err := RAIDDeletable(c.blk, taken); !errors.Is(err, c.want) {
			t.Errorf("%s: %v, want %v", name, err, c.want)
		}
	}
}

func TestTheMountedPartsOfAnArrayAreThoseTheCallerUnmounts(t *testing.T) {
	blk := model.LSBLKModel{Path: "/dev/md0", MountPoint: "/media/a", Children: []model.LSBLKModel{{Path: "/dev/md0p1", MountPoint: "/media/b"}, {Path: "/dev/md0p2"}}}
	var got []string
	for _, v := range RAIDVolumes(blk) {
		got = append(got, v.MountPoint)
	}
	if want := []string{"/media/a", "/media/b"}; !reflect.DeepEqual(got, want) {
		t.Errorf("volumes %q, want %q", got, want)
	}
	if len(RAIDVolumes(model.LSBLKModel{Path: "/dev/md1"})) != 0 {
		t.Error("an array nothing mounts has volumes")
	}
}
