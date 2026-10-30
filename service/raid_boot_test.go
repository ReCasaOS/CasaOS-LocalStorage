package service

import (
	"testing"

	"github.com/ReCasaOS/CasaOS-Common/utils/logger"
	"github.com/ReCasaOS/CasaOS-LocalStorage/model"
)

func raidMemberDisk(name string, children ...model.LSBLKModel) model.LSBLKModel {
	return model.LSBLKModel{Name: name, Path: "/dev/" + name, Type: "disk", FsType: "linux_raid_member", Children: children}
}

var raidArrayBelow = model.LSBLKModel{Name: "md0", Path: "/dev/md0", Type: "raid1", FsType: "ext4"}

func TestAMemberWithoutItsArrayIsSeen(t *testing.T) {
	partition := func(children ...model.LSBLKModel) model.LSBLKModel {
		return model.LSBLKModel{Name: "sdb", Type: "disk", Children: []model.LSBLKModel{{Name: "sdb1", Type: "part", FsType: "linux_raid_member", Children: children}}}
	}
	for name, c := range map[string]struct {
		disks []model.LSBLKModel
		want  bool
	}{
		"no disk":                           {nil, false},
		"a plain disk":                      {[]model.LSBLKModel{{Name: "sda", Type: "disk", FsType: "ext4"}}, false},
		"members under their array":         {[]model.LSBLKModel{raidMemberDisk("sdb", raidArrayBelow), raidMemberDisk("sdc", raidArrayBelow)}, false},
		"a member whose array is not there": {[]model.LSBLKModel{raidMemberDisk("sdb", raidArrayBelow), raidMemberDisk("sdc")}, true},
		"a member partition, no array":      {[]model.LSBLKModel{partition()}, true},
		"a member partition, its array":     {[]model.LSBLKModel{partition(raidArrayBelow)}, false},
		"an array of another level":         {[]model.LSBLKModel{raidMemberDisk("sdb", model.LSBLKModel{Name: "md1", Type: "raid10"})}, false},
		"a partition that is not a raid":    {[]model.LSBLKModel{raidMemberDisk("sdb", model.LSBLKModel{Name: "sdb1", Type: "part"})}, true},
	} {
		if got := raidMemberWithoutArray(c.disks); got != c.want {
			t.Errorf("%s: %v, want %v", name, got, c.want)
		}
	}
}

func TestTheBootWaitsForAnArrayOnlyAsLongAsItIsMissing(t *testing.T) {
	logger.LogInitConsoleOnly()
	waits := 0
	wait := func() { waits++ }

	// the array shows up at the third reading
	reads := 0
	list := func() []model.LSBLKModel {
		reads++
		if reads == 3 {
			return []model.LSBLKModel{raidMemberDisk("sdb", raidArrayBelow)}
		}
		return []model.LSBLKModel{raidMemberDisk("sdb")}
	}
	got := awaitRAIDArrays([]model.LSBLKModel{raidMemberDisk("sdb")}, list, wait, 15)
	if waits != 3 || reads != 3 || raidMemberWithoutArray(got) {
		t.Errorf("waits %d, reads %d, still missing %v", waits, reads, raidMemberWithoutArray(got))
	}

	// a member whose array never comes costs the whole time and no more
	waits = 0
	got = awaitRAIDArrays([]model.LSBLKModel{raidMemberDisk("sdb")}, func() []model.LSBLKModel { return []model.LSBLKModel{raidMemberDisk("sdb")} }, wait, 15)
	if waits != 15 || !raidMemberWithoutArray(got) {
		t.Errorf("waits %d", waits)
	}

	// nothing to wait for: the disks are not read again
	waits = 0
	awaitRAIDArrays([]model.LSBLKModel{raidMemberDisk("sdb", raidArrayBelow)}, func() []model.LSBLKModel { t.Fatal("read again"); return nil }, wait, 15)
	if waits != 0 {
		t.Errorf("waits %d", waits)
	}
}
