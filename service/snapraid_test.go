package service

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ReCasaOS/CasaOS-Common/utils/logger"
)

const snapraidDiffOutput = `Loading state from /x/snapraid.content...
Comparing...
add a.bin
remove b.txt

       2 equal
       1 added
      %d removed
       0 updated
       0 moved
       0 copied
       0 restored
There are differences!
`

// snapraidBox puts a box where /mnt/d1, /mnt/d2 and /mnt/p1 are three mounts, and snapraid is a fake that answers by the subcommand it is given. It gives back
// what was run and what the dashboard was told.
type snapraidBox struct {
	ran     [][]string
	told    []string
	outputs map[string]string
	codes   map[string]int
	mounts  map[string]string
	now     time.Time
}

func newSnapRAIDBox(t *testing.T) *snapraidBox {
	t.Helper()
	logger.LogInitConsoleOnly()
	box := &snapraidBox{
		outputs: map[string]string{},
		codes:   map[string]int{},
		mounts:  map[string]string{"/mnt/d1": "8:1", "/mnt/d2": "8:17", "/mnt/p1": "8:33", "/mnt/p2": "8:49"},
		now:     time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC),
	}
	savedDir, savedHas, savedMounts, savedUsage, savedExec, savedNotify, savedNow := snapraidDir, hasSnapRAID, snapraidMounts, snapraidUsage, snapraidExec, snapraidNotify, snapraidNow
	snapraidNow = func() time.Time { return box.now }
	dir := t.TempDir()
	snapraidDir = func() string { return dir }
	hasSnapRAID = func() bool { return true }
	snapraidMounts = func() (map[string]string, error) { return box.mounts, nil }
	snapraidUsage = func(path string) (uint64, uint64, uint64, error) {
		if strings.HasPrefix(path, "/mnt/p") {
			return 100, 90, 10, nil
		}
		return 100, 50, 50, nil
	}
	snapraidExec = func(ctx context.Context, args []string, line func(string)) (int, error) {
		box.ran = append(box.ran, args)
		for _, a := range args {
			switch a {
			case "diff", "sync", "scrub", "fix":
				for _, l := range strings.Split(box.outputs[a], "\n") {
					line(l)
				}
				return box.codes[a], nil
			}
		}
		return 0, nil
	}
	snapraidNotify = func(action string, result SnapRAIDResult) {
		box.told = append(box.told, action+":"+map[bool]string{true: "ok", false: "no"}[result.OK])
	}
	snapraidJob, snapraidStop = nil, nil
	t.Cleanup(func() {
		snapraidDir, hasSnapRAID, snapraidMounts, snapraidUsage, snapraidExec, snapraidNotify, snapraidNow = savedDir, savedHas, savedMounts, savedUsage, savedExec, savedNotify, savedNow
		snapraidJob, snapraidStop = nil, nil
	})
	return box
}

func (b *snapraidBox) subcommands() []string {
	var out []string
	for _, args := range b.ran {
		for _, a := range args {
			if a == "diff" || a == "sync" || a == "scrub" || a == "fix" {
				out = append(out, a)
			}
		}
	}
	return out
}

func setUpSnapRAID(t *testing.T) SnapRAIDConfig {
	t.Helper()
	cfg, err := SetSnapRAID([]string{"/mnt/d1", "/mnt/d2"}, []string{"/mnt/p1"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestTheConfigurationIsWrittenForSnapRAID(t *testing.T) {
	newSnapRAIDBox(t)
	cfg, err := SetSnapRAID([]string{"/mnt/d1", "/mnt/d2"}, []string{"/mnt/p1", "/mnt/p2"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MaxRemoved != 40 || cfg.Hour != 3 {
		t.Fatalf("defaults: %+v", cfg)
	}
	conf, err := os.ReadFile(snapraidConfPath())
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"parity /mnt/p1/snapraid.parity\n", "2-parity /mnt/p2/snapraid.parity\n",
		"content " + filepath.Join(snapraidDir(), "snapraid.content") + "\n", "content /mnt/p1/snapraid.content\n", "content /mnt/p2/snapraid.content\n",
		"data d1 /mnt/d1/\n", "data d2 /mnt/d2/\n",
	} {
		if !strings.Contains(string(conf), want) {
			t.Errorf("missing %q in\n%s", want, conf)
		}
	}
}

func TestADiskKeepsItsNameWhenTheOthersChange(t *testing.T) {
	box := newSnapRAIDBox(t)
	box.mounts["/mnt/d3"] = "8:65"
	first, _ := SetSnapRAID([]string{"/mnt/d1", "/mnt/d2"}, []string{"/mnt/p1"}, nil, nil)
	// d2 leaves and d3 arrives: d3 does not take the name d2 had
	second, err := SetSnapRAID([]string{"/mnt/d1", "/mnt/d3"}, []string{"/mnt/p1"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if first.Data[0].Name != "d1" || second.Data[0].Name != "d1" || second.Data[1].Name != "d3" {
		t.Fatalf("first %+v second %+v", first.Data, second.Data)
	}
}

func TestDisksThatCannotBeProtectedAreRefused(t *testing.T) {
	box := newSnapRAIDBox(t)
	box.mounts["/mnt/d3"] = "8:1" // the same device as d1
	for name, c := range map[string]struct {
		data, parity []string
		want         error
	}{
		"no parity":        {[]string{"/mnt/d1"}, nil, ErrSnapRAIDParity},
		"three parity":     {[]string{"/mnt/d1"}, []string{"/mnt/p1", "/mnt/p2", "/mnt/d2"}, ErrSnapRAIDParity},
		"no data":          {nil, []string{"/mnt/p1"}, ErrSnapRAIDData},
		"a folder":         {[]string{"/mnt/d1", "/home/gary/docs"}, []string{"/mnt/p1"}, ErrSnapRAIDNotMounted},
		"the same device":  {[]string{"/mnt/d1", "/mnt/d3"}, []string{"/mnt/p1"}, ErrSnapRAIDSameDisk},
		"data as parity":   {[]string{"/mnt/d1"}, []string{"/mnt/d1"}, ErrSnapRAIDSameDisk},
		"a path with dots": {[]string{"/mnt/d1/../d1", "/mnt/d1"}, []string{"/mnt/p1"}, ErrSnapRAIDSameDisk},
	} {
		if _, err := SetSnapRAID(c.data, c.parity, nil, nil); !errors.Is(err, c.want) {
			t.Errorf("%s: %v, want %v", name, err, c.want)
		}
	}
	if _, err := os.Stat(snapraidStatePath()); err == nil {
		t.Error("a refused setup left a setup")
	}

	// the parity disk has 90 free, the fullest data disk 150 used
	snapraidUsage = func(path string) (uint64, uint64, uint64, error) {
		if strings.HasPrefix(path, "/mnt/p") {
			return 200, 90, 110, nil
		}
		return 200, 50, 150, nil
	}
	if _, err := SetSnapRAID([]string{"/mnt/d1"}, []string{"/mnt/p1"}, nil, nil); !errors.Is(err, ErrSnapRAIDParitySize) {
		t.Errorf("a small parity disk: %v", err)
	}
}

func TestMountInfoGivesTheDeviceOfEachMountPoint(t *testing.T) {
	got := parseMountInfo("36 35 98:0 / /mnt/my\\040disk rw,noatime - ext4 /dev/sdb1 rw\n37 35 0:41 / /tmp/x rw - tmpfs tmpfs rw\nshort\n")
	if got["/mnt/my disk"] != "98:0" || got["/tmp/x"] != "0:41" || len(got) != 2 {
		t.Fatalf("%v", got)
	}
}

func TestASyncThatForgetsManyFilesIsHeldBack(t *testing.T) {
	box := newSnapRAIDBox(t)
	setUpSnapRAID(t)
	box.outputs["diff"], box.codes["diff"] = strings.Replace(snapraidDiffOutput, "%d", "41", 1), 2

	result, err := runSnapRAIDNow("sync", SnapRAIDOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if result.OK || !result.Blocked || result.Diff.Removed != 41 || !strings.Contains(result.Message, "41 files were removed") {
		t.Fatalf("%+v", result)
	}
	if got := box.subcommands(); len(got) != 1 || got[0] != "diff" {
		t.Fatalf("a held back sync ran %v", got)
	}
	if last := SnapRAIDStatus().Last["sync"]; !last.Blocked {
		t.Fatalf("the reason was not kept: %+v", last)
	}

	// said to go on, it goes on
	result, err = runSnapRAIDNow("sync", SnapRAIDOptions{Force: true})
	if err != nil || !result.OK {
		t.Fatalf("%+v %v", result, err)
	}
	if got := strings.Join(box.subcommands(), " "); got != "diff diff sync" {
		t.Fatalf("ran %s", got)
	}
}

func TestASyncOfFewRemovalsGoesOn(t *testing.T) {
	box := newSnapRAIDBox(t)
	setUpSnapRAID(t)
	box.outputs["diff"], box.codes["diff"] = strings.Replace(snapraidDiffOutput, "%d", "40", 1), 2
	result, _ := runSnapRAIDNow("sync", SnapRAIDOptions{})
	if !result.OK || result.Blocked || strings.Join(box.subcommands(), " ") != "diff sync" {
		t.Fatalf("%+v ran %v", result, box.subcommands())
	}
	if strings.Join(box.told, ",") != "sync:ok" {
		t.Fatalf("told %v", box.told)
	}
}

func TestAFailureIsToldInSnapRAIDsWordsWithoutTheNoise(t *testing.T) {
	box := newSnapRAIDBox(t)
	setUpSnapRAID(t)
	box.outputs["scrub"] = "Self test...\nLoading state from /x...\nScrubbing...\n8%, 0 MB\nWARNING! Unexpected file errors!\n100% completed\n     d1  0% | \n       1 file errors\n       0 io errors\nSaving state to /x...\nVerifying..."
	box.codes["scrub"] = 1
	result, _ := runSnapRAIDNow("scrub", SnapRAIDOptions{})
	if result.OK || result.Message != "Scrubbing...\nWARNING! Unexpected file errors!\n1 file errors\n0 io errors" {
		t.Fatalf("%+v", result)
	}
	if strings.Join(box.told, ",") != "scrub:no" {
		t.Fatalf("told %v", box.told)
	}
}

func TestAFullScrubReadsAllOfIt(t *testing.T) {
	box := newSnapRAIDBox(t)
	setUpSnapRAID(t)
	runSnapRAIDNow("scrub", SnapRAIDOptions{Full: true})
	if last := strings.Join(box.ran[len(box.ran)-1], " "); !strings.HasSuffix(last, "scrub -p 100 -o 0") {
		t.Fatalf("%s", last)
	}
}

func TestNothingRunsWhileADiskIsNotWhereItShouldBe(t *testing.T) {
	box := newSnapRAIDBox(t)
	setUpSnapRAID(t)
	delete(box.mounts, "/mnt/d2")
	for _, action := range []string{"diff", "sync", "scrub", "fix"} {
		_, err := StartSnapRAID(action, SnapRAIDOptions{Confirm: "all"})
		if !errors.Is(err, ErrSnapRAIDNotMounted) || !strings.Contains(err.Error(), "d2") {
			t.Errorf("%s: %v", action, err)
		}
	}
	if len(box.ran) != 0 {
		t.Fatalf("ran %v", box.ran)
	}
	if got := SnapRAIDStatus().Missing; len(got) != 1 || got[0] != "d2" {
		t.Fatalf("missing %v", got)
	}
}

func TestFixNeedsTheDiskTypedAgain(t *testing.T) {
	box := newSnapRAIDBox(t)
	setUpSnapRAID(t)
	for name, opts := range map[string]SnapRAIDOptions{
		"nothing typed":    {Disk: "d1"},
		"another disk":     {Disk: "d1", Confirm: "d2"},
		"all not typed":    {},
		"a disk not there": {Disk: "d9", Confirm: "d9"},
	} {
		if _, err := StartSnapRAID("fix", opts); err == nil {
			t.Errorf("%s: started", name)
		}
	}
	if len(box.ran) != 0 {
		t.Fatalf("ran %v", box.ran)
	}

	result, err := runSnapRAIDNow("fix", SnapRAIDOptions{Disk: "d1", Confirm: "d1"})
	if err != nil || !result.OK {
		t.Fatalf("%+v %v", result, err)
	}
	if last := strings.Join(box.ran[len(box.ran)-1], " "); !strings.HasSuffix(last, "-d d1 fix") {
		t.Fatalf("%s", last)
	}
}

func TestOneActionAtATime(t *testing.T) {
	newSnapRAIDBox(t)
	setUpSnapRAID(t)
	started := make(chan struct{})
	release := make(chan struct{})
	snapraidExec = func(ctx context.Context, args []string, line func(string)) (int, error) {
		line("42%, 10 MB")
		close(started)
		select {
		case <-release:
		case <-ctx.Done():
		}
		return 0, nil
	}
	if _, err := StartSnapRAID("scrub", SnapRAIDOptions{}); err != nil {
		t.Fatal(err)
	}
	<-started
	if _, err := StartSnapRAID("sync", SnapRAIDOptions{}); !errors.Is(err, ErrSnapRAIDBusy) {
		t.Fatalf("a second action: %v", err)
	}
	if _, err := SetSnapRAID([]string{"/mnt/d1"}, []string{"/mnt/p1"}, nil, nil); !errors.Is(err, ErrSnapRAIDBusy) {
		t.Fatalf("a change of setup at work: %v", err)
	}
	if err := ClearSnapRAID(); !errors.Is(err, ErrSnapRAIDBusy) {
		t.Fatalf("a clear at work: %v", err)
	}
	if job := SnapRAIDStatus().Job; job == nil || job.Action != "scrub" || job.Percent != 42 {
		t.Fatalf("job %+v", job)
	}

	// stopped, it ends and says so
	if !StopSnapRAID() {
		t.Fatal("nothing to stop")
	}
	deadline := time.Now().Add(5 * time.Second)
	for SnapRAIDStatus().Job != nil && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if SnapRAIDStatus().Job != nil {
		t.Fatal("the job did not end")
	}
}

func TestTheNightlySyncRunsOnceAtItsHour(t *testing.T) {
	box := newSnapRAIDBox(t)
	setUpSnapRAID(t)
	box.outputs["diff"], box.codes["diff"] = strings.Replace(snapraidDiffOutput, "%d", "0", 1), 2
	day := time.Date(2026, 11, 3, 3, 0, 0, 0, time.UTC) // a Tuesday

	run := func(at time.Time) {
		box.now = at
		RunSnapRAIDSchedule(at)
	}
	run(day.Add(-time.Hour)) // 2 o'clock
	if len(box.ran) != 0 {
		t.Fatalf("ran before its hour: %v", box.ran)
	}
	run(day)
	if got := strings.Join(box.subcommands(), " "); got != "diff sync" {
		t.Fatalf("ran %q", got)
	}
	run(day.Add(10 * time.Minute)) // the hour again, after a restart
	if len(box.subcommands()) != 2 {
		t.Fatalf("ran twice: %v", box.subcommands())
	}

	// a Sunday scrubs as well, once
	sunday := time.Date(2026, 11, 8, 3, 0, 0, 0, time.UTC)
	run(sunday)
	if got := strings.Join(box.subcommands(), " "); got != "diff sync diff sync scrub" {
		t.Fatalf("ran %q", got)
	}
}

func TestNoScheduleWhenTheHourIsMinusOne(t *testing.T) {
	box := newSnapRAIDBox(t)
	off := -1
	if _, err := SetSnapRAID([]string{"/mnt/d1"}, []string{"/mnt/p1"}, nil, &off); err != nil {
		t.Fatal(err)
	}
	for h := 0; h < 24; h++ {
		RunSnapRAIDSchedule(time.Date(2026, 11, 3, h, 0, 0, 0, time.UTC))
	}
	if len(box.ran) != 0 {
		t.Fatalf("ran %v", box.ran)
	}
}

func TestAScheduledSyncThatCannotStartLeavesItsReason(t *testing.T) {
	box := newSnapRAIDBox(t)
	setUpSnapRAID(t)
	delete(box.mounts, "/mnt/p1")
	box.now = time.Date(2026, 11, 3, 3, 0, 0, 0, time.UTC)
	RunSnapRAIDSchedule(box.now)
	last := SnapRAIDStatus().Last["sync"]
	if last.OK || !strings.Contains(last.Message, "not mounted") || len(box.ran) != 0 {
		t.Fatalf("%+v ran %v", last, box.ran)
	}
}

func TestClearingForgetsTheSetupAndNothingElse(t *testing.T) {
	newSnapRAIDBox(t)
	setUpSnapRAID(t)
	if err := ClearSnapRAID(); err != nil {
		t.Fatal(err)
	}
	if state := SnapRAIDStatus(); state.Configured || state.Config != nil {
		t.Fatalf("%+v", state)
	}
	if _, err := StartSnapRAID("sync", SnapRAIDOptions{}); !errors.Is(err, ErrSnapRAIDNotSet) {
		t.Fatalf("%v", err)
	}
	if err := ClearSnapRAID(); err != nil {
		t.Fatalf("clearing twice: %v", err)
	}
}

// The real binary, on four tmpfs mounts (so four devices): a sync, a disk that loses a file and gets it back, and the guard. Needs root and snapraid.
func TestTheRealSnapRAIDProtectsAndRestores(t *testing.T) {
	if _, err := exec.LookPath("snapraid"); err != nil || os.Geteuid() != 0 {
		t.Skip("needs snapraid and root (to mount tmpfs)")
	}
	root := t.TempDir()
	mount := func(name string) string {
		dir := filepath.Join(root, name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if out, err := exec.Command("mount", "-t", "tmpfs", "-o", "size=40m", "tmpfs", dir).CombinedOutput(); err != nil {
			t.Skipf("no tmpfs: %v %s", err, out)
		}
		t.Cleanup(func() { _ = exec.Command("umount", dir).Run() })
		return dir
	}
	d1, d2, p1 := mount("d1"), mount("d2"), mount("p1")
	dir := t.TempDir()
	savedDir, savedNotify := snapraidDir, snapraidNotify
	snapraidDir = func() string { return dir }
	snapraidNotify = func(string, SnapRAIDResult) {}
	t.Cleanup(func() { snapraidDir, snapraidNotify = savedDir, savedNotify })

	body := strings.Repeat("the data of the first disk ", 40000)
	if err := os.WriteFile(filepath.Join(d1, "a.txt"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d1, "b.txt"), []byte("b"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d2, "c.txt"), []byte("the second disk"), 0o644); err != nil {
		t.Fatal(err)
	}
	none := 0
	if _, err := SetSnapRAID([]string{d1, d2}, []string{p1}, &none, nil); err != nil {
		t.Fatal(err)
	}

	result, err := runSnapRAIDNow("diff", SnapRAIDOptions{})
	if err != nil || !result.OK || result.Diff.Added != 3 {
		t.Fatalf("diff: %+v %v", result, err)
	}
	if result, err = runSnapRAIDNow("sync", SnapRAIDOptions{}); err != nil || !result.OK {
		t.Fatalf("sync: %+v %v", result, err)
	}

	// two files removed, none allowed: held back
	if err := os.Remove(filepath.Join(d1, "b.txt")); err != nil {
		t.Fatal(err)
	}
	if result, _ = runSnapRAIDNow("sync", SnapRAIDOptions{}); !result.Blocked || result.Diff.Removed != 1 {
		t.Fatalf("guard: %+v", result)
	}
	if result, _ = runSnapRAIDNow("sync", SnapRAIDOptions{Force: true}); !result.OK {
		t.Fatalf("forced sync: %+v", result)
	}

	// a.txt is lost with its disk's content: fix puts it back from the parity
	if err := os.Remove(filepath.Join(d1, "a.txt")); err != nil {
		t.Fatal(err)
	}
	if result, err = runSnapRAIDNow("fix", SnapRAIDOptions{Disk: "d1", Confirm: "d1"}); err != nil || !result.OK {
		t.Fatalf("fix: %+v %v", result, err)
	}
	got, err := os.ReadFile(filepath.Join(d1, "a.txt"))
	if err != nil || string(got) != body {
		t.Fatalf("a.txt came back as %d bytes, %v", len(got), err)
	}
	if result, err = runSnapRAIDNow("scrub", SnapRAIDOptions{Full: true}); err != nil || !result.OK {
		t.Fatalf("scrub: %+v %v", result, err)
	}
}
