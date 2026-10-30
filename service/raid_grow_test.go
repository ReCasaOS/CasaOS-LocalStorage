package service

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func mirrorOfTwo(bitmap bool) RAIDArray {
	return RAIDArray{Name: "md0", Path: "/dev/md0", Level: "raid1", State: "ok", Disks: 2, Action: "idle", Bitmap: bitmap, ComponentSize: 1000, Size: 1000}
}

func TestTheSizeAndTheBitmapOfAnArrayAreRead(t *testing.T) {
	sys := sysfs(t, merge(
		array("0", map[string]string{"level": "raid1", "array_state": "clean", "raid_disks": "2", "degraded": "0", "bitmap/location": "+8"}, nil),
		array("1", map[string]string{"level": "raid1", "array_state": "clean", "raid_disks": "2", "degraded": "0", "bitmap/location": "none"}, nil),
		array("2", map[string]string{"level": "raid5", "array_state": "clean", "raid_disks": "3", "degraded": "0"}, nil),
		map[string]string{"block/md0/size": "2000", "block/md1/size": "4000"},
	), nil)

	got := map[string]RAIDArray{}
	for _, a := range RAIDArrays(sys) {
		got[a.Name] = a
	}
	if a := got["md0"]; a.Size != 2000*512 || !a.Bitmap {
		t.Errorf("a mirror with a bitmap: %+v", a)
	}
	if a := got["md1"]; a.Size != 4000*512 || a.Bitmap {
		t.Errorf("bitmap/location says none: %+v", a)
	}
	if a := got["md2"]; a.Size != 0 || a.Bitmap {
		t.Errorf("no size and no bitmap folder: %+v", a)
	}
}

func TestAMirrorBecomesARAID5(t *testing.T) {
	for name, c := range map[string]struct {
		bitmap bool
		want   []string
	}{
		"without a bitmap": {false, []string{
			"wipefs --all /dev/sdd",
			"mdadm --grow /dev/md0 --level=5",
			"mdadm --manage /dev/md0 --add /dev/sdd",
			"mdadm --grow /dev/md0 --raid-devices=3 --backup-file=/b/md0.backup",
		}},
		"with a bitmap, taken off first": {true, []string{
			"wipefs --all /dev/sdd",
			"mdadm --grow /dev/md0 --bitmap=none",
			"mdadm --grow /dev/md0 --level=5",
			"mdadm --manage /dev/md0 --add /dev/sdd",
			"mdadm --grow /dev/md0 --raid-devices=3 --backup-file=/b/md0.backup",
		}},
	} {
		ran := recordRuns(t, true, "")
		if err := RAIDToRAID5(mirrorOfTwo(c.bitmap), blank, "/b/md0.backup"); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !reflect.DeepEqual(*ran, c.want) {
			t.Errorf("%s: ran\n%s\nwant\n%s", name, strings.Join(*ran, "\n"), strings.Join(c.want, "\n"))
		}
	}
}

// What cannot be changed is refused before anything is installed, wiped or run.
func TestOnlyAHealthyMirrorOfTwoDisksIsChanged(t *testing.T) {
	for name, c := range map[string]struct {
		change func(*RAIDArray)
		want   error
	}{
		"a RAID 5":               {func(a *RAIDArray) { a.Level = "raid5" }, ErrRAIDNotMirror},
		"a RAID 10":              {func(a *RAIDArray) { a.Level = "raid10" }, ErrRAIDNotMirror},
		"a mirror of three":      {func(a *RAIDArray) { a.Disks = 3 }, ErrRAIDNotMirror},
		"a degraded mirror":      {func(a *RAIDArray) { a.State, a.Missing = "degraded", 1 }, ErrRAIDNotSettled},
		"a mirror resyncing":     {func(a *RAIDArray) { a.State, a.Action = "resyncing", "resync" }, ErrRAIDNotSettled},
		"a mirror being checked": {func(a *RAIDArray) { a.State, a.Action = "checking", "check" }, ErrRAIDNotSettled},
		"a mirror not running":   {func(a *RAIDArray) { a.State = "inactive" }, ErrRAIDNotSettled},
	} {
		ran := recordRuns(t, false, "")
		array := mirrorOfTwo(true)
		c.change(&array)
		if err := RAIDToRAID5(array, blank, "/b"); !errors.Is(err, c.want) {
			t.Errorf("%s: %v, want %v", name, err, c.want)
		}
		if len(*ran) != 0 {
			t.Errorf("%s: ran %v", name, *ran)
		}
	}
}

func TestWithoutMDADMNoDiskIsWiped(t *testing.T) {
	ran := recordRuns(t, false, "apt-get")
	if err := RAIDToRAID5(mirrorOfTwo(false), blank, "/b"); !errors.Is(err, ErrNoMDADM) {
		t.Errorf("%v, want ErrNoMDADM", err)
	}
	if !reflect.DeepEqual(*ran, []string{"apt-get install mdadm"}) {
		t.Errorf("ran %v: no disk may be wiped when mdadm cannot be had", *ran)
	}
}

// A step that fails brings the array back to a RAID 1 with its bitmap, and the error says so; what is undone is in the commands.
func TestAFailedChangeIsUndone(t *testing.T) {
	for name, c := range map[string]struct {
		fail    string
		bitmap  bool
		undone  []string
		mention string
	}{
		"the level cannot change, with a bitmap": {"mdadm --grow /dev/md0 --level=5", true,
			[]string{"mdadm --grow /dev/md0 --bitmap=internal"}, "changing /dev/md0 to RAID 5"},
		"the level cannot change, without": {"mdadm --grow /dev/md0 --level=5", false, nil, "changing /dev/md0 to RAID 5"},
		"the disk cannot be added": {"mdadm --manage", true,
			[]string{"mdadm --grow /dev/md0 --level=1", "mdadm --grow /dev/md0 --bitmap=internal"}, "/dev/md0 is a RAID 1 again"},
		"the reshape does not start": {"mdadm --grow /dev/md0 --raid-devices", false,
			[]string{"mdadm --grow /dev/md0 --level=1"}, "reshaping /dev/md0 onto /dev/sdd"},
	} {
		ran := recordRuns(t, true, c.fail)
		err := RAIDToRAID5(mirrorOfTwo(c.bitmap), blank, "/b")
		if err == nil || !strings.Contains(err.Error(), c.mention) {
			t.Errorf("%s: %v, want it to say %q", name, err, c.mention)
		}
		last := (*ran)[len(*ran)-len(c.undone):]
		if len(c.undone) > 0 && !reflect.DeepEqual(last, c.undone) {
			t.Errorf("%s: the last commands were %v, want %v", name, last, c.undone)
		}
	}
}

func TestTheFilesystemTakesTheRoomOfTheArray(t *testing.T) {
	big := func(a *RAIDArray) { a.Level, a.Size = "raid5", raidBitmapMinimum }
	for name, c := range map[string]struct {
		change func(*RAIDArray)
		want   []string
	}{
		"a small RAID 5":                    {func(a *RAIDArray) { a.Level, a.Size = "raid5", 1<<30 }, []string{"resize2fs /dev/md0"}},
		"a big RAID 5 with no bitmap":       {big, []string{"resize2fs /dev/md0", "mdadm --grow /dev/md0 --bitmap=internal"}},
		"a big RAID 5 that has its bitmap":  {func(a *RAIDArray) { big(a); a.Bitmap = true }, []string{"resize2fs /dev/md0"}},
		"a big mirror, which keeps its way": {func(a *RAIDArray) { a.Size = raidBitmapMinimum }, []string{"resize2fs /dev/md0"}},
	} {
		ran := recordRuns(t, true, "")
		array := mirrorOfTwo(false)
		c.change(&array)
		backup := filepath.Join(t.TempDir(), "md0.backup")
		if err := os.WriteFile(backup, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := GrowRAIDFilesystem(array, "ext4", backup); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !reflect.DeepEqual(*ran, c.want) {
			t.Errorf("%s: ran %v, want %v", name, *ran, c.want)
		}
		if _, err := os.Stat(backup); !os.IsNotExist(err) {
			t.Errorf("%s: the backup file of the reshape is still there", name)
		}
	}
}

func TestTheFilesystemIsNotGrownWhereItCannotBe(t *testing.T) {
	for name, c := range map[string]struct {
		fs     string
		change func(*RAIDArray)
		want   error
	}{
		"xfs":              {"xfs", func(*RAIDArray) {}, ErrRAIDFilesystem},
		"no filesystem":    {"", func(*RAIDArray) {}, ErrRAIDFilesystem},
		"a reshape on":     {"ext4", func(a *RAIDArray) { a.State, a.Action = "reshaping", "reshape" }, ErrRAIDNotSettled},
		"a degraded array": {"ext4", func(a *RAIDArray) { a.State = "degraded" }, ErrRAIDNotSettled},
	} {
		ran := recordRuns(t, true, "")
		array := mirrorOfTwo(false)
		c.change(&array)
		if err := GrowRAIDFilesystem(array, c.fs, "/b"); !errors.Is(err, c.want) {
			t.Errorf("%s: %v, want %v", name, err, c.want)
		}
		if len(*ran) != 0 {
			t.Errorf("%s: ran %v", name, *ran)
		}
	}
}
