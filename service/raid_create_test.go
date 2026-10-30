package service

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/ReCasaOS/CasaOS-LocalStorage/model"
)

func blankDisk(name string) model.LSBLKModel {
	return model.LSBLKModel{Name: name, Path: "/dev/" + name, Type: "disk", Tran: "sata", Size: 2000}
}

func TestAnArrayIsMadeOfTheDisksNamedUnderTheFirstFreeName(t *testing.T) {
	ran := recordRuns(t, true, "")
	// sde has an old partition: its signature goes first, then the disk's
	path, err := CreateRAIDArray([]RAIDArray{{Name: "md0"}}, "raid1", []model.LSBLKModel{blank, old})
	if err != nil || path != "/dev/md1" {
		t.Fatalf("%q %v", path, err)
	}
	want := []string{
		"wipefs --all /dev/sdd",
		"wipefs --all /dev/sde1",
		"wipefs --all /dev/sde",
		"mdadm --create /dev/md1 --run --metadata=1.2 --level=1 --raid-devices=2 /dev/sdd /dev/sde",
		"udevadm settle",
	}
	if !reflect.DeepEqual(*ran, want) {
		t.Errorf("ran:\n%s\nwant:\n%s", strings.Join(*ran, "\n"), strings.Join(want, "\n"))
	}
}

func TestEachLevelIsMadeAtItsOwnNumberOfDisks(t *testing.T) {
	for level, c := range map[string]struct {
		disks []string
		want  string
	}{
		"raid5":  {[]string{"sdb", "sdc", "sdd"}, "mdadm --create /dev/md0 --run --metadata=1.2 --level=5 --raid-devices=3 /dev/sdb /dev/sdc /dev/sdd"},
		"raid6":  {[]string{"sdb", "sdc", "sdd", "sde"}, "mdadm --create /dev/md0 --run --metadata=1.2 --level=6 --raid-devices=4 /dev/sdb /dev/sdc /dev/sdd /dev/sde"},
		"raid10": {[]string{"sdb", "sdc", "sdd", "sde"}, "mdadm --create /dev/md0 --run --metadata=1.2 --level=10 --raid-devices=4 /dev/sdb /dev/sdc /dev/sdd /dev/sde"},
	} {
		ran := recordRuns(t, true, "")
		var disks []model.LSBLKModel
		for _, n := range c.disks {
			disks = append(disks, blankDisk(n))
		}
		if _, err := CreateRAIDArray(nil, level, disks); err != nil {
			t.Fatalf("%s: %v", level, err)
		}
		if got := (*ran)[len(*ran)-2]; got != c.want {
			t.Errorf("%s: %q, want %q", level, got, c.want)
		}
	}
}

// A refusal comes before the first disk is touched: nothing is wiped for an array that will not be made.
func TestNothingIsWipedForAnArrayThatCannotBeMade(t *testing.T) {
	two := []model.LSBLKModel{blankDisk("sdb"), blankDisk("sdc")}
	for name, c := range map[string]struct {
		mdadm bool
		level string
		disks []model.LSBLKModel
		want  error
	}{
		"raid0 loses all with one disk":  {true, "raid0", two, ErrRAIDLevel},
		"linear is not offered":          {true, "linear", two, ErrRAIDLevel},
		"no level":                       {true, "", two, ErrRAIDLevel},
		"raid5 needs three disks":        {true, "raid5", two, ErrRAIDTooFewDisks},
		"raid1 needs two disks":          {true, "raid1", two[:1], ErrRAIDTooFewDisks},
		"a disk named twice":             {true, "raid1", []model.LSBLKModel{two[0], two[0]}, ErrRAIDSameDisk},
		"mdadm is not installed":         {false, "raid1", two, ErrNoMDADM},
		"raid10 needs four disks, not 3": {true, "raid10", append(two, blankDisk("sdd")), ErrRAIDTooFewDisks},
		"raid6 needs four disks, not 3":  {true, "raid6", append(two, blankDisk("sdd")), ErrRAIDTooFewDisks},
		"no disk at all":                 {true, "raid1", nil, ErrRAIDTooFewDisks},
	} {
		ran := recordRuns(t, c.mdadm, "")
		if _, err := CreateRAIDArray(nil, c.level, c.disks); !errors.Is(err, c.want) {
			t.Errorf("%s: %v, want %v", name, err, c.want)
		}
		if len(*ran) != 0 {
			t.Errorf("%s: ran %v", name, *ran)
		}
	}
}

func TestADiskThatCannotBeWipedStopsBeforeTheArrayIsMade(t *testing.T) {
	ran := recordRuns(t, true, "wipefs --all /dev/sdc")
	_, err := CreateRAIDArray(nil, "raid1", []model.LSBLKModel{blankDisk("sdb"), blankDisk("sdc")})
	if err == nil || !strings.Contains(err.Error(), "wiping /dev/sdc") {
		t.Errorf("%v", err)
	}
	for _, line := range *ran {
		if strings.HasPrefix(line, "mdadm") {
			t.Errorf("the array was made: %v", *ran)
		}
	}
}

func TestAnArrayMdadmRefusesIsAnError(t *testing.T) {
	recordRuns(t, true, "mdadm --create")
	path, err := CreateRAIDArray(nil, "raid1", []model.LSBLKModel{blankDisk("sdb"), blankDisk("sdc")})
	if path != "" || err == nil || !strings.Contains(err.Error(), "making /dev/md0") {
		t.Errorf("%q %v", path, err)
	}
}

func TestTheNameOfANewArrayIsTheFirstOneFree(t *testing.T) {
	for _, c := range []struct {
		arrays []RAIDArray
		want   string
	}{
		{nil, "md0"},
		{[]RAIDArray{{Name: "md0"}}, "md1"},
		{[]RAIDArray{{Name: "md0"}, {Name: "md2"}}, "md1"},
		{[]RAIDArray{{Name: "md0"}, {Name: "md1"}, {Name: "md2"}}, "md3"},
		{[]RAIDArray{{Name: "md127"}}, "md0"},
	} {
		if got := nextRAIDName(c.arrays); got != c.want {
			t.Errorf("%v: %s, want %s", c.arrays, got, c.want)
		}
	}
}
