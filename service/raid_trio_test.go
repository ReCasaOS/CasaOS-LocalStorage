package service

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/ReCasaOS/CasaOS-LocalStorage/model"
)

func trioOfThree(bitmap bool) RAIDArray {
	return RAIDArray{Name: "md0", Path: "/dev/md0", Level: "raid1", State: "ok", Disks: 3, Action: "idle", Bitmap: bitmap, ComponentSize: 1000, Size: 1000,
		Members: []RAIDMember{
			{Name: "sdb", Disk: "sdb", State: "active", Slot: 0},
			{Name: "sdc", Disk: "sdc", State: "active", Slot: 1},
			{Name: "sdd", Disk: "sdd", State: "active", Slot: 2},
		}}
}

var (
	// sdd holds the last drive of the mirror, whole, and the array that is mounted
	trioDisk = model.LSBLKModel{Name: "sdd", Path: "/dev/sdd", Type: "disk", Tran: "sata", Size: 2000,
		Children: []model.LSBLKModel{{Name: "md0", Path: "/dev/md0", Type: "raid1", MountPoint: "/media/test"}}}
	// or a partition of it, sdd1
	trioPartDisk = model.LSBLKModel{Name: "sdd", Path: "/dev/sdd", Type: "disk", Tran: "sata", Size: 2000,
		Children: []model.LSBLKModel{{Name: "sdd1", Path: "/dev/sdd1", Type: "part", Children: []model.LSBLKModel{{Name: "md0", Path: "/dev/md0", Type: "raid1"}}}}}
	trioMember = RAIDMember{Name: "sdd", Disk: "sdd", State: "active", Slot: 2}
)

func TestTheSlotOfAMemberIsRead(t *testing.T) {
	sys := sysfs(t, merge(
		array("0", map[string]string{"level": "raid1", "array_state": "clean", "raid_disks": "3", "degraded": "0"},
			map[string][2]string{"sdb": {"in_sync", "0"}, "sdc": {"in_sync", "1"}, "sdd": {"in_sync", "2"}, "sde": {"spare", "none"}}),
	), partitions)
	slots := map[string]int{}
	for _, m := range RAIDArrays(sys)[0].Members {
		slots[m.Name] = m.Slot
	}
	if want := map[string]int{"sdb": 0, "sdc": 1, "sdd": 2, "sde": -1}; !reflect.DeepEqual(slots, want) {
		t.Errorf("slots %v, want %v", slots, want)
	}
}

func TestAMirrorOfThreeGivesUpItsLastDriveToBecomeARAID5(t *testing.T) {
	// the drive taken out, erased and put back as the third; dev is what is failed and zeroed (the partition, when it is one), wipes what is wiped after
	runs := func(dev string, wipes []string, bitmap bool) []string {
		out := []string{
			"mdadm --manage /dev/md0 --fail " + dev,
			"mdadm --manage /dev/md0 --remove " + dev,
			"mdadm --grow /dev/md0 --raid-devices=2",
			"mdadm --zero-superblock " + dev,
		}
		out = append(out, wipes...)
		if bitmap {
			out = append(out, "mdadm --grow /dev/md0 --bitmap=none")
		}
		return append(out,
			"mdadm --grow /dev/md0 --level=5",
			"mdadm --manage /dev/md0 --add /dev/sdd",
			"mdadm --grow /dev/md0 --raid-devices=3 --backup-file=/b/md0.backup",
		)
	}
	for name, c := range map[string]struct {
		bitmap bool
		disk   model.LSBLKModel
		member RAIDMember
		want   []string
	}{
		"a whole disk, without a bitmap": {false, trioDisk, trioMember, runs("/dev/sdd", []string{"wipefs --all /dev/sdd"}, false)},
		"a whole disk, with a bitmap":    {true, trioDisk, trioMember, runs("/dev/sdd", []string{"wipefs --all /dev/sdd"}, true)},
		"a partition": {false, trioPartDisk, RAIDMember{Name: "sdd1", Disk: "sdd", State: "active", Slot: 2},
			runs("/dev/sdd1", []string{"wipefs --all /dev/sdd1", "wipefs --all /dev/sdd"}, false)},
	} {
		array := trioOfThree(c.bitmap)
		array.Members[2] = c.member
		disk, member, err := RAIDFreeableDisk([]model.LSBLKModel{c.disk}, array, "/dev/sdd")
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		ran := recordRuns(t, true, "")
		if err := RAIDTrioToRAID5(array, disk, member, "/b/md0.backup"); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !reflect.DeepEqual(*ran, c.want) {
			t.Errorf("%s: ran\n%s\nwant\n%s", name, strings.Join(*ran, "\n"), strings.Join(c.want, "\n"))
		}
	}
}

// What cannot be freed is refused before anything is installed, failed or wiped: the function that says which drive it is does not run a command.
func TestOnlyTheLastDriveOfAHealthyMirrorOfThreeIsFreed(t *testing.T) {
	for name, c := range map[string]struct {
		change func(*RAIDArray)
		path   string
		disk   model.LSBLKModel
		want   error
	}{
		"a mirror of two": {func(a *RAIDArray) { a.Disks, a.Members = 2, a.Members[:2] }, "/dev/sdd", trioDisk, ErrRAIDNotMirror},
		"a RAID 5":        {func(a *RAIDArray) { a.Level = "raid5" }, "/dev/sdd", trioDisk, ErrRAIDNotMirror},
		"a spare besides": {func(a *RAIDArray) {
			a.Members = append(a.Members, RAIDMember{Name: "sde", Disk: "sde", State: "spare", Slot: -1})
		}, "/dev/sdd", trioDisk, ErrRAIDNotMirror},
		"a degraded mirror":          {func(a *RAIDArray) { a.State, a.Missing = "degraded", 1 }, "/dev/sdd", trioDisk, ErrRAIDNotSettled},
		"a mirror resyncing":         {func(a *RAIDArray) { a.State, a.Action = "resyncing", "resync" }, "/dev/sdd", trioDisk, ErrRAIDNotSettled},
		"the first drive":            {func(*RAIDArray) {}, "/dev/sdb", trioDisk, ErrRAIDNotMember},
		"a disk that is not one":     {func(*RAIDArray) {}, "/dev/sdz", trioDisk, ErrRAIDNotMember},
		"no member in the last slot": {func(a *RAIDArray) { a.Members[2].Slot = 7 }, "/dev/sdd", trioDisk, ErrRAIDNotMember},
		"another partition on it": {func(*RAIDArray) {}, "/dev/sdd", model.LSBLKModel{Name: "sdd", Path: "/dev/sdd", Type: "disk", Tran: "sata",
			Children: []model.LSBLKModel{{Name: "md0", Path: "/dev/md0", Type: "raid1"}, {Name: "sdd2", Path: "/dev/sdd2", Type: "part", MountPoint: "/DATA/x"}}}, ErrRAIDDiskNotFree},
		"the system disk": {func(*RAIDArray) {}, "/dev/sdd", model.LSBLKModel{Name: "sdd", Path: "/dev/sdd", Type: "disk", Tran: "sata",
			Children: []model.LSBLKModel{{Name: "md0", Path: "/dev/md0", Type: "raid1", MountPoint: "/"}}}, ErrRAIDDiskNotFree},
		"a kind of disk not supported": {func(*RAIDArray) {}, "/dev/sdd", model.LSBLKModel{Name: "sdd", Path: "/dev/sdd", Type: "disk", Tran: "ata-over-ethernet"}, ErrRAIDDiskNotFree},
		"a disk the box does not list": {func(*RAIDArray) {}, "/dev/sdd", model.LSBLKModel{Name: "sdq", Path: "/dev/sdq", Type: "disk", Tran: "sata"}, ErrRAIDDiskNotFree},
	} {
		ran := recordRuns(t, true, "")
		array := trioOfThree(true)
		c.change(&array)
		if _, _, err := RAIDFreeableDisk([]model.LSBLKModel{c.disk}, array, c.path); !errors.Is(err, c.want) {
			t.Errorf("%s: %v, want %v", name, err, c.want)
		}
		if len(*ran) != 0 {
			t.Errorf("%s: ran %v", name, *ran)
		}
	}
}

func TestTheOtherDriveIsNamedWhenTheWrongOneIsAsked(t *testing.T) {
	_, _, err := RAIDFreeableDisk([]model.LSBLKModel{trioDisk}, trioOfThree(false), "/dev/sdb")
	if !errors.Is(err, ErrRAIDNotMember) || !strings.Contains(err.Error(), "sdd") {
		t.Errorf("%v: it should say that sdd is the one", err)
	}
}

// Each step that fails says where it leaves the array, and puts the drive back while it is still possible.
func TestAFailedChangeOfAMirrorOfThreeIsSaid(t *testing.T) {
	for name, c := range map[string]struct {
		fail    string
		last    string
		mention string
	}{
		"the drive cannot be failed":    {"mdadm --manage /dev/md0 --fail", "mdadm --manage /dev/md0 --fail /dev/sdd", "taking /dev/sdd out of /dev/md0"},
		"the drive cannot be taken out": {"mdadm --manage /dev/md0 --remove", "mdadm --manage /dev/md0 --re-add /dev/sdd", "/dev/sdd is back in /dev/md0"},
		"the mirror cannot be cut down": {"mdadm --grow /dev/md0 --raid-devices=2", "mdadm --manage /dev/md0 --re-add /dev/sdd", "/dev/sdd is back in /dev/md0"},
		"the drive cannot be wiped":     {"wipefs --all /dev/sdd", "wipefs --all /dev/sdd", "/dev/md0 is a RAID 1 of two drives now"},
		"the level cannot change":       {"mdadm --grow /dev/md0 --level=5", "mdadm --grow /dev/md0 --bitmap=internal", "changing /dev/md0 to RAID 5"},
		"the reshape does not start":    {"mdadm --grow /dev/md0 --raid-devices=3", "mdadm --grow /dev/md0 --bitmap=internal", "/dev/md0 is a RAID 1 again"},
	} {
		ran := recordRuns(t, true, c.fail)
		err := RAIDTrioToRAID5(trioOfThree(true), trioDisk, trioMember, "/b")
		if err == nil || !strings.Contains(err.Error(), c.mention) {
			t.Errorf("%s: %v, want it to say %q", name, err, c.mention)
		}
		if last := (*ran)[len(*ran)-1]; last != c.last {
			t.Errorf("%s: the last command was %q, want %q", name, last, c.last)
		}
	}
}

func TestWithoutMDADMNoDriveIsFailed(t *testing.T) {
	ran := recordRuns(t, false, "apt-get")
	err := RAIDTrioToRAID5(trioOfThree(false), trioDisk, trioMember, "/b")
	if !errors.Is(err, ErrNoMDADM) || !reflect.DeepEqual(*ran, []string{"apt-get install mdadm"}) {
		t.Errorf("%v, ran %v: nothing may be failed or wiped when mdadm cannot be had", err, *ran)
	}
}
