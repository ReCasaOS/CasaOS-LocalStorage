package service

import (
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// RAIDArray is a Linux software RAID array (md) as the kernel shows it under
// /sys/block/<name>/md: read only, nothing is asked of mdadm.
type RAIDArray struct {
	Name  string `json:"name"`  // md0
	Path  string `json:"path"`  // /dev/md0
	Level string `json:"level"` // raid1, raid5, raid10…
	// State sums the array up: ok, degraded (members missing or failed),
	// rebuilding (a member is being rebuilt), resyncing, checking, reshaping,
	// or inactive (not running: its data cannot be reached).
	State string `json:"state"`
	// Disks is how many members the array is made of, Missing how many of them
	// it lacks now (failed, removed or being rebuilt).
	Disks   int `json:"disks"`
	Missing int `json:"missing"`
	// ComponentSize is the bytes each member gives the array: a disk added
	// must hold at least that.
	ComponentSize int64 `json:"component_size"`
	// Action is what the array is doing (idle, recover, resync, check, repair,
	// reshape), Progress its percent and Remaining the seconds left; -1 when
	// there is none or it cannot be told.
	Action    string       `json:"action"`
	Progress  float64      `json:"progress"`
	Remaining int64        `json:"remaining"`
	Members   []RAIDMember `json:"members"`
}

// RAIDMember is a device of an array.
type RAIDMember struct {
	Name string `json:"name"` // sdb1
	Disk string `json:"disk"` // sdb: the disk it is on
	// State is active, rebuilding, spare or faulty.
	State string `json:"state"`
}

// RAIDArrays lists the md arrays under the sysfs at sys ("/sys"), by name.
func RAIDArrays(sys string) []RAIDArray {
	blocks, _ := filepath.Glob(filepath.Join(sys, "block", "md*", "md"))
	arrays := []RAIDArray{}
	for _, mdDir := range blocks {
		level := readSys(mdDir, "level")
		if level == "" {
			continue
		}
		name := filepath.Base(filepath.Dir(mdDir))
		array := RAIDArray{
			Name:    name,
			Path:    "/dev/" + name,
			Level:   level,
			Disks:   atoiSys(mdDir, "raid_disks"),
			Missing: atoiSys(mdDir, "degraded"),
			// component_size is in KiB
			ComponentSize: int64(atoiSys(mdDir, "component_size")) * 1024,
			Action:        readSys(mdDir, "sync_action"),
			Progress:      -1,
			Remaining:     -1,
			Members:       raidMembers(sys, mdDir),
		}
		if array.Action == "" {
			array.Action = "idle"
		}
		if done, total, ok := syncCompleted(readSys(mdDir, "sync_completed")); ok && array.Action != "idle" {
			array.Progress = float64(done) * 100 / float64(total)
			if speed, err := strconv.ParseInt(readSys(mdDir, "sync_speed"), 10, 64); err == nil && speed > 0 {
				array.Remaining = (total - done) * 512 / (speed * 1024) // sectors of 512 bytes, speed in KiB/s
			}
		}
		array.State = raidState(readSys(mdDir, "array_state"), array)
		arrays = append(arrays, array)
	}
	sort.Slice(arrays, func(i, j int) bool { return arrays[i].Name < arrays[j].Name })
	return arrays
}

func raidState(arrayState string, a RAIDArray) string {
	switch {
	// broken: a RAID 0 or linear array that lost a member (Linux 5.4 and later): it has no redundancy, its data is gone with the disk
	case arrayState == "inactive" || arrayState == "clear" || arrayState == "broken" || arrayState == "":
		return "inactive"
	case a.Missing > 0 && (a.Action == "recover" || a.Action == "resync"):
		return "rebuilding"
	case a.Missing > 0:
		return "degraded"
	case a.Action == "resync":
		return "resyncing"
	case a.Action == "check" || a.Action == "repair":
		return "checking"
	case a.Action == "reshape":
		return "reshaping"
	}
	for _, m := range a.Members {
		if m.State == "faulty" { // a spare took its place: the array is whole, the disk still has to go
			return "degraded"
		}
	}
	return "ok"
}

// raidMembers reads the dev-* folders of an array.
func raidMembers(sys, mdDir string) []RAIDMember {
	devs, _ := filepath.Glob(filepath.Join(mdDir, "dev-*"))
	members := []RAIDMember{}
	for _, dev := range devs {
		name := strings.TrimPrefix(filepath.Base(dev), "dev-")
		flags := strings.Split(readSys(dev, "state"), ",")
		has := func(flag string) bool {
			for _, f := range flags {
				if f == flag {
					return true
				}
			}
			return false
		}
		slot := readSys(dev, "slot")
		state := "active"
		switch {
		case has("faulty"):
			state = "faulty"
		case slot == "none" || slot == "":
			state = "spare"
		case !has("in_sync"):
			state = "rebuilding"
		}
		members = append(members, RAIDMember{Name: name, Disk: diskOf(sys, name), State: state})
	}
	return members
}

// diskOf is the disk a block device is on: its parent for a partition, itself
// otherwise.
func diskOf(sys, name string) string {
	real, err := filepath.EvalSymlinks(filepath.Join(sys, "class", "block", name))
	if err != nil {
		return name
	}
	if _, err := os.Stat(filepath.Join(real, "partition")); err == nil {
		return filepath.Base(filepath.Dir(real))
	}
	return name
}

// syncCompleted reads "done / total" in sectors; false for "none" or garbage.
func syncCompleted(value string) (int64, int64, bool) {
	done, total, ok := strings.Cut(value, "/")
	if !ok {
		return 0, 0, false
	}
	d, err1 := strconv.ParseInt(strings.TrimSpace(done), 10, 64)
	t, err2 := strconv.ParseInt(strings.TrimSpace(total), 10, 64)
	if err1 != nil || err2 != nil || t <= 0 || d < 0 || d > t {
		return 0, 0, false
	}
	return d, t, true
}

func readSys(dir, name string) string {
	data, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func atoiSys(dir, name string) int {
	n, _ := strconv.Atoi(readSys(dir, name))
	return n
}
