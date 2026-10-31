package service

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// sysfs builds a fake /sys: files is path -> content under it, links is
// link -> target (both relative to it).
func sysfs(t *testing.T, files map[string]string, links map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for name, content := range files {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for link, target := range links {
		path := filepath.Join(root, link)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(root, target), path); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// array is the sysfs of md<n>: its md files, and members name -> {state, slot}.
func array(n string, md map[string]string, members map[string][2]string) map[string]string {
	files := map[string]string{}
	for k, v := range md {
		files["block/md"+n+"/md/"+k] = v
	}
	for name, m := range members {
		files["block/md"+n+"/md/dev-"+name+"/state"] = m[0]
		files["block/md"+n+"/md/dev-"+name+"/slot"] = m[1]
	}
	return files
}

func merge(maps ...map[string]string) map[string]string {
	out := map[string]string{}
	for _, m := range maps {
		for k, v := range m {
			out[k] = v
		}
	}
	return out
}

// partitions: sdb1 and sdc1 are partitions of sdb and sdc, sdd is a whole disk.
var partitions = map[string]string{
	"devices/pci/sdb/sdb1/partition": "1",
	"devices/pci/sdc/sdc1/partition": "1",
	"devices/pci/sdd/size":           "1",
	"devices/pci/sde/size":           "1",
}

var partitionLinks = map[string]string{
	"class/block/sdb1": "devices/pci/sdb/sdb1",
	"class/block/sdc1": "devices/pci/sdc/sdc1",
	"class/block/sdd":  "devices/pci/sdd",
	"class/block/sde":  "devices/pci/sde",
}

func TestTheStateOfAnArrayIsReadFromSysfs(t *testing.T) {
	sys := sysfs(t, merge(partitions,
		array("0", map[string]string{"level": "raid1", "array_state": "clean", "raid_disks": "2", "degraded": "0", "sync_action": "idle", "sync_completed": "none", "component_size": "976630464"},
			map[string][2]string{"sdb1": {"in_sync", "0"}, "sdc1": {"in_sync", "1"}}),
		array("1", map[string]string{"level": "raid1", "array_state": "clean", "raid_disks": "2", "degraded": "1", "sync_action": "idle"},
			map[string][2]string{"sdb1": {"in_sync", "0"}}),
		array("2", map[string]string{"level": "raid5", "array_state": "active", "raid_disks": "3", "degraded": "1", "sync_action": "recover", "sync_completed": "1000000 / 3000000", "sync_speed": "1024"},
			map[string][2]string{"sdb1": {"in_sync", "0"}, "sdc1": {"in_sync", "1"}, "sdd": {"", "2"}}),
		array("3", map[string]string{"level": "raid1", "array_state": "clean", "raid_disks": "2", "degraded": "0", "sync_action": "idle"},
			map[string][2]string{"sdb1": {"in_sync", "0"}, "sdc1": {"in_sync", "1"}, "sdd": {"faulty", "none"}, "sde": {"spare", "none"}}),
		array("4", map[string]string{"level": "raid1", "array_state": "inactive", "raid_disks": "2"}, nil),
		array("5", map[string]string{"level": "raid6", "array_state": "clean", "raid_disks": "4", "degraded": "0", "sync_action": "check", "sync_completed": "250 / 1000", "sync_speed": "none"}, nil),
		array("6", map[string]string{"level": "raid0", "array_state": "broken", "raid_disks": "2"}, map[string][2]string{"sdb1": {"in_sync", "0"}}),
		map[string]string{"block/md9/size": "0"}, // an md device without an array behind it
	), partitionLinks)

	arrays := RAIDArrays(sys)
	got := map[string]RAIDArray{}
	for _, a := range arrays {
		got[a.Name] = a
	}
	if len(arrays) != 7 || arrays[0].Name != "md0" || arrays[6].Name != "md6" {
		t.Fatalf("%d arrays: %+v", len(arrays), arrays)
	}

	if a := got["md0"]; a.State != "ok" || a.Path != "/dev/md0" || a.Level != "raid1" || a.Disks != 2 || a.Missing != 0 || a.Progress != -1 || a.Remaining != -1 || a.ComponentSize != 976630464*1024 ||
		!reflect.DeepEqual(a.Members, []RAIDMember{{"sdb1", "sdb", "active", 0}, {"sdc1", "sdc", "active", 1}}) {
		t.Errorf("a healthy mirror: %+v", a)
	}
	if a := got["md1"]; a.State != "degraded" || a.Missing != 1 || len(a.Members) != 1 {
		t.Errorf("a mirror with one disk gone: %+v", a)
	}
	if a := got["md2"]; a.State != "rebuilding" || a.Action != "recover" || a.Progress < 33.3 || a.Progress > 33.4 || a.Remaining != 976 ||
		a.Members[2] != (RAIDMember{"sdd", "sdd", "rebuilding", 2}) {
		t.Errorf("a RAID 5 rebuilding a whole disk: %+v", a)
	}
	if a := got["md3"]; a.State != "degraded" || a.Missing != 0 || a.Members[2].State != "faulty" || a.Members[3].State != "spare" {
		t.Errorf("a failed disk a spare stood in for: %+v", a)
	}
	if a := got["md4"]; a.State != "inactive" || len(a.Members) != 0 {
		t.Errorf("an array that does not run: %+v", a)
	}
	if a := got["md5"]; a.State != "checking" || a.Progress != 25 || a.Remaining != -1 {
		t.Errorf("a check without a speed: %+v", a)
	}
	if a := got["md6"]; a.State != "inactive" {
		t.Errorf("a RAID 0 that lost a disk: %+v", a)
	}
}

func TestNoArrayIsAnEmptyList(t *testing.T) {
	if arrays := RAIDArrays(t.TempDir()); arrays == nil || len(arrays) != 0 {
		t.Fatalf("%#v", arrays)
	}
}

func TestSyncCompleted(t *testing.T) {
	for value, want := range map[string]bool{"1 / 2": true, "none": false, "3 / 2": false, "x / 2": false, "1 / 0": false, "": false} {
		if _, _, ok := syncCompleted(value); ok != want {
			t.Errorf("syncCompleted(%q) = %v", value, ok)
		}
	}
}
