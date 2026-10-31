package service

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/ReCasaOS/CasaOS-LocalStorage/model"
)

// recordRuns swaps the commands for a recorder; fail makes the command whose
// line starts with it fail.
func recordRuns(t *testing.T, mdadm bool, fail string) *[]string {
	t.Helper()
	var ran []string
	savedRun, savedHas, savedInstall, savedWait := raidRun, hasMDADM, raidInstall, raidRetryWait
	raidRetryWait = 0
	raidRun = func(name string, args ...string) error {
		line := strings.Join(append([]string{name}, args...), " ")
		ran = append(ran, line)
		if fail != "" && strings.HasPrefix(line, fail) {
			return errors.New("refused")
		}
		return nil
	}
	hasMDADM = func() bool { return mdadm }
	// the installation is recorded like any command, as "apt-get install <package>"
	raidInstall = func(pkg string) error { return raidRun("apt-get", "install", pkg) }
	t.Cleanup(func() { raidRun, hasMDADM, raidInstall, raidRetryWait = savedRun, savedHas, savedInstall, savedWait })
	return &ran
}

var (
	blank   = model.LSBLKModel{Name: "sdd", Path: "/dev/sdd", Type: "disk", Tran: "sata", Size: 2000}
	old     = model.LSBLKModel{Name: "sde", Path: "/dev/sde", Type: "disk", Tran: "sata", Size: 2000, Children: []model.LSBLKModel{{Name: "sde1", Path: "/dev/sde1", Type: "part", FsType: "ext4"}}}
	inUse   = model.LSBLKModel{Name: "sdf", Path: "/dev/sdf", Type: "disk", Tran: "sata", Size: 2000, Children: []model.LSBLKModel{{Name: "sdf1", Path: "/dev/sdf1", Type: "part", FsType: "ext4", MountPoint: "/DATA/x"}}}
	member  = model.LSBLKModel{Name: "sdg", Path: "/dev/sdg", Type: "disk", Tran: "sata", Size: 2000, FsType: "linux_raid_member"}
	small   = model.LSBLKModel{Name: "sdh", Path: "/dev/sdh", Type: "disk", Tran: "sata", Size: 999}
	sysDisk = model.LSBLKModel{Name: "sda", Path: "/dev/sda", Type: "disk", Tran: "sata", Size: 9000, Children: []model.LSBLKModel{{Name: "sda1", Path: "/dev/sda1", Type: "part", MountPoint: "/"}}}
	part    = model.LSBLKModel{Name: "sdd1", Path: "/dev/sdd1", Type: "part", Tran: "sata", Size: 2000}
)

func TestOnlyABlankDiskBigEnoughMayJoinAnArray(t *testing.T) {
	disks := []model.LSBLKModel{blank, old, inUse, member, small, sysDisk, part}
	for path, want := range map[string]error{
		"/dev/sdd":  nil,
		"/dev/sde":  nil, // an old filesystem nothing mounts: it is wiped
		"/dev/sdf":  ErrRAIDDiskNotFree,
		"/dev/sdg":  ErrRAIDDiskNotFree, // a member of an array, assembled or not
		"/dev/sdh":  ErrRAIDDiskTooSmall,
		"/dev/sda":  ErrRAIDDiskNotFree,
		"/dev/sdd1": ErrRAIDDiskNotFree, // a partition, not a disk
		"/dev/sdz":  ErrRAIDDiskNotFree,
		"/etc/x":    ErrRAIDDiskNotFree,
	} {
		if _, err := RAIDCandidate(disks, path, 1000); !errors.Is(err, want) {
			t.Errorf("%s: %v, want %v", path, err, want)
		}
	}
}

func degradedMirror() RAIDArray {
	return RAIDArray{Name: "md0", Path: "/dev/md0", Level: "raid1", State: "degraded", Disks: 2, Missing: 1,
		Members: []RAIDMember{{Name: "sdb1", Disk: "sdb", State: "active"}, {Name: "sdc1", Disk: "sdc", State: "faulty"}}}
}

func TestADiskIsWipedThenAdded(t *testing.T) {
	ran := recordRuns(t, true, "")
	if err := AddRAIDDisk(degradedMirror(), old); err != nil {
		t.Fatal(err)
	}
	want := []string{"wipefs --all /dev/sde1", "wipefs --all /dev/sde", "mdadm --manage /dev/md0 --add /dev/sde"}
	if !reflect.DeepEqual(*ran, want) {
		t.Fatalf("ran %q, want %q", *ran, want)
	}
}

func TestAWipeThatFailsAddsNothing(t *testing.T) {
	ran := recordRuns(t, true, "wipefs --all /dev/sdd")
	if err := AddRAIDDisk(degradedMirror(), blank); err == nil {
		t.Fatal("added")
	}
	if len(*ran) != 1 {
		t.Fatalf("ran %q", *ran)
	}
}

func TestADiskIsAddedOnlyWhereOneIsLacking(t *testing.T) {
	ran := recordRuns(t, true, "")
	whole := degradedMirror()
	whole.State, whole.Missing, whole.Members[1].State = "ok", 0, "active"
	stopped := degradedMirror()
	stopped.State = "inactive"
	spareStoodIn := degradedMirror() // a spare rebuilt the array, the failed disk is still in it
	spareStoodIn.Missing = 0
	for _, c := range []struct {
		array RAIDArray
		want  error
	}{{whole, ErrRAIDWhole}, {stopped, ErrRAIDNotRunning}, {spareStoodIn, nil}} {
		if err := AddRAIDDisk(c.array, blank); !errors.Is(err, c.want) {
			t.Errorf("%+v: %v, want %v", c.array, err, c.want)
		}
	}
	if len(*ran) != 2 { // the spare case only: wipe, add
		t.Fatalf("ran %q", *ran)
	}
	recordRuns(t, false, "")
	if err := AddRAIDDisk(degradedMirror(), blank); !errors.Is(err, ErrNoMDADM) {
		t.Fatalf("without mdadm: %v", err)
	}
}

func TestOnlyAFailedDiskIsTakenOut(t *testing.T) {
	ran := recordRuns(t, true, "")
	if err := RemoveRAIDDisk(degradedMirror(), "sdb1"); !errors.Is(err, ErrRAIDNotFaulty) {
		t.Fatalf("an active disk: %v", err)
	}
	if err := RemoveRAIDDisk(degradedMirror(), "sdx1"); !errors.Is(err, ErrRAIDNotFaulty) {
		t.Fatalf("no such disk: %v", err)
	}
	if err := RemoveRAIDDisk(degradedMirror(), "sdc1"); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(*ran, []string{"mdadm --manage /dev/md0 --remove /dev/sdc1"}) {
		t.Fatalf("ran %q", *ran)
	}
}
