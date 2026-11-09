package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func zfsFixture(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "zfs", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func zfsClearCache() {
	zfsCache.Lock()
	zfsCache.state, zfsCache.flight = nil, nil
	zfsCache.Unlock()
}

// zfsReset puts the package back as it was when the test ends.
func zfsReset(t *testing.T) {
	t.Helper()
	exec, has, resolve, now, grace := zfsExec, zfsHas, zfsResolveDisk, zfsNow, zfsKillGrace
	zfsClearCache()
	zfsResolveDisk = func(path string) string { return "nvme0n1" }
	t.Cleanup(func() {
		zfsExec, zfsHas, zfsResolveDisk, zfsNow, zfsKillGrace = exec, has, resolve, now, grace
		zfsClearCache()
	})
}

func parseStatus(t *testing.T, fixture string) ZFSPool {
	t.Helper()
	zfsReset(t)
	pool := ZFSPool{Name: "p", DataErrors: -1}
	parseZpoolStatus(zfsFixture(t, fixture), &pool)
	return pool
}

func TestParseZpoolList(t *testing.T) {
	pools := parseZpoolList(zfsFixture(t, "zpool_list_two.txt"))
	if len(pools) != 2 {
		t.Fatalf("%+v", pools)
	}
	p := pools[0]
	if p.Name != "pool1" || p.Size != 1992864825344 || p.Allocated != 1060842668032 || p.Free != 932022157312 || p.Fragmentation != 29 || p.Capacity != 53 || p.Health != "ONLINE" {
		t.Fatalf("%+v", p)
	}
	if pools[1].Fragmentation != -1 || pools[1].Health != "DEGRADED" {
		t.Fatalf("a '-' is not a number: %+v", pools[1])
	}
	if got := parseZpoolList(zfsFixture(t, "zpool_list_none.txt")); len(got) != 0 {
		t.Fatalf("'no pools available' is no pool: %+v", got)
	}
	if got := parseZpoolList(""); got == nil || len(got) != 0 {
		t.Fatalf("%+v", got)
	}
}

func TestParseStatusHealthyMirror(t *testing.T) {
	p := parseStatus(t, "zpool_status_healthy_mirror.txt")
	if !p.Known || p.State != "ONLINE" || p.DataErrors != 0 || p.Status != "" {
		t.Fatalf("%+v", p)
	}
	s := p.Scan
	want := time.Date(2026, 10, 11, 0, 40, 48, 0, time.Local).Unix()
	if s.Kind != "scrub" || s.State != "finished" || s.Repaired != "0B" || s.Errors != 0 || s.Duration != 16*60+47 || s.Ended != want {
		t.Fatalf("%+v", s)
	}
	if len(p.Vdevs) != 1 {
		t.Fatalf("the pool itself is not a vdev of the tree: %+v", p.Vdevs)
	}
	m := p.Vdevs[0]
	if m.Name != "mirror-0" || m.Kind != "mirror" || m.State != "ONLINE" || len(m.Children) != 2 {
		t.Fatalf("%+v", m)
	}
	d := m.Children[0]
	if d.Kind != "disk" || d.Disk != "nvme0n1" || d.Read != 0 || d.Write != 0 || d.Cksum != 0 || !strings.HasSuffix(d.Name, "806391_1-part1") {
		t.Fatalf("%+v", d)
	}
}

func TestParseStatusDegradedWithResilver(t *testing.T) {
	p := parseStatus(t, "zpool_status_degraded_raidz.txt")
	if !p.Known || p.State != "DEGRADED" || p.DataErrors != 2 {
		t.Fatalf("%+v", p)
	}
	if !strings.HasPrefix(p.Status, "One or more devices could not be opened.") || !strings.Contains(p.Status, "degraded state.") || !strings.HasPrefix(p.Action, "Attach the missing device") || !strings.HasSuffix(p.See, "ZFS-8000-2Q") {
		t.Fatalf("the words of ZFS are kept, joined: %q / %q / %q", p.Status, p.Action, p.See)
	}
	s := p.Scan
	if s.Kind != "resilver" || s.State != "running" || s.Percent != 43.2 || s.Remaining != 20*60+11 || s.Started == 0 {
		t.Fatalf("%+v", s)
	}
	if len(p.Vdevs) != 1 || p.Vdevs[0].Kind != "raidz" || p.Vdevs[0].State != "DEGRADED" || len(p.Vdevs[0].Children) != 3 {
		t.Fatalf("%+v", p.Vdevs)
	}
	kids := p.Vdevs[0].Children
	if kids[1].Write != 2 {
		t.Fatalf("%+v", kids[1])
	}
	if kids[2].State != "UNAVAIL" || kids[2].Note != "was /dev/sdc1" || kids[2].Disk != "" || kids[2].Name != "11485761428236503841" {
		t.Fatalf("a disk that is gone is still in the tree: %+v", kids[2])
	}
}

func TestParseStatusScrubRunningWithNoEstimate(t *testing.T) {
	s := parseStatus(t, "zpool_status_scrub_running.txt").Scan
	if s.Kind != "scrub" || s.State != "running" || s.Percent != 21.7 || s.Remaining != -1 {
		t.Fatalf("%+v", s)
	}
}

func TestParseStatusGroups(t *testing.T) {
	p := parseStatus(t, "zpool_status_groups.txt")
	names := []string{}
	for _, v := range p.Vdevs {
		names = append(names, v.Name+":"+v.Kind)
	}
	if strings.Join(names, " ") != "mirror-0:mirror mirror-1:mirror logs:group cache:group spares:group" {
		t.Fatalf("%v", names)
	}
	if len(p.Vdevs[0].Children) != 2 || len(p.Vdevs[2].Children) != 1 || p.Vdevs[2].Children[0].Name != "/dev/sde" {
		t.Fatalf("%+v", p.Vdevs)
	}
	if spare := p.Vdevs[4].Children[0]; spare.State != "AVAIL" || spare.Read != -1 {
		t.Fatalf("a spare has no counters: %+v", spare)
	}
	if p.Scan.Kind != "scrub" || p.Scan.State != "canceled" || p.Scan.Ended == 0 {
		t.Fatalf("%+v", p.Scan)
	}
}

func TestParseStatusKeepsGoingOverWhatItDoesNotKnow(t *testing.T) {
	zfsReset(t)
	text := zfsFixture(t, "zpool_status_healthy_mirror.txt")
	text = strings.Replace(text, "config:", "a line from a newer version of ZFS\nconfig:", 1)
	text = strings.Replace(text, "errors:", "\tsome new thing\nerrors:", 1)
	pool := ZFSPool{DataErrors: -1}
	parseZpoolStatus(text, &pool)
	if !pool.Known || pool.State != "ONLINE" || len(pool.Vdevs) != 1 || pool.DataErrors != 0 {
		t.Fatalf("%+v", pool)
	}
}

func TestParseStatusTruncatedOrEmpty(t *testing.T) {
	zfsReset(t)
	pool := ZFSPool{DataErrors: -1}
	parseZpoolStatus("  pool: tank\n state: ONLINE\n", &pool)
	if !pool.Known || pool.State != "ONLINE" || len(pool.Vdevs) != 0 || pool.DataErrors != -1 || pool.Scan.Kind != "none" {
		t.Fatalf("%+v", pool)
	}
	pool = ZFSPool{DataErrors: -1}
	parseZpoolStatus("", &pool)
	if pool.Known {
		t.Fatalf("nothing was read, the pool is not known: %+v", pool)
	}
	pool = ZFSPool{DataErrors: -1}
	parseZpoolStatus("cannot open 'x': no such pool\n", &pool)
	if pool.Known {
		t.Fatalf("%+v", pool)
	}
}

func TestParseScanNoneRequested(t *testing.T) {
	pool := ZFSPool{}
	parseZpoolStatus("  pool: t\n state: ONLINE\n  scan: none requested\nconfig:\n\n", &pool)
	if pool.Scan.Kind != "none" || pool.Scan.Raw != "" {
		t.Fatalf("%+v", pool.Scan)
	}
}

func TestParseScanElsewhere(t *testing.T) {
	scan := parseZfsScan([]string{"something new happened"})
	if scan.Kind != "other" || scan.Raw != "something new happened" {
		t.Fatalf("%+v", scan)
	}
	if got := zfsDurationSeconds("1 days 02:03:04"); got != 86400+2*3600+3*60+4 {
		t.Fatalf("%d", got)
	}
}

func TestParseZfsList(t *testing.T) {
	datasets := parseZfsList(zfsFixture(t, "zfs_list_datasets.txt"))
	if len(datasets) != 6 {
		t.Fatalf("%+v", datasets)
	}
	root, app, vol, secret := datasets[0], datasets[2], datasets[4], datasets[5]
	if root.Name != "pool1" || root.Pool != "pool1" || root.Type != "filesystem" || root.Mountpoint != "/pool1" || !root.Mounted || root.Quota != 0 || root.KeyStatus != "" {
		t.Fatalf("%+v", root)
	}
	if app.Pool != "pool1" || app.Used != 300647710720 || app.UsedBySnapshots != 31457280 || app.Referenced != 300647710720 || app.Available != 671000000000 {
		t.Fatalf("%+v", app)
	}
	if vol.Type != "volume" || vol.Mountpoint != "" || vol.Mounted {
		t.Fatalf("a volume has no mount point: %+v", vol)
	}
	if secret.KeyStatus != "unavailable" || secret.Mounted || secret.Quota != 1073741824 {
		t.Fatalf("%+v", secret)
	}
	if got := parseZfsList(zfsFixture(t, "zfs_list_root_only.txt")); len(got) != 1 || got[0].Name != "pool1" {
		t.Fatalf("a pool with no datasets of its own: %+v", got)
	}
	if got := parseZfsList(zfsFixture(t, "zfs_list_none.txt")); got == nil || len(got) != 0 {
		t.Fatalf("'no datasets available' is no dataset: %+v", got)
	}
}

func TestParseZfsSnapshots(t *testing.T) {
	list := parseZfsSnapshots(zfsFixture(t, "zfs_list_snapshots.txt"))
	if len(list) != 2 || list[0].Name != "backup_20261105_023008" || list[0].Used != 12373196 || list[0].Written != 17000000 || list[0].Created != 1762309808 || list[1].Written != 299000000000 {
		t.Fatalf("%+v", list)
	}
	if got := parseZfsSnapshots("not a snapshot\t1\t2\t3\t4\n"); len(got) != 0 {
		t.Fatalf("%+v", got)
	}
}

func TestParseKstatIO(t *testing.T) {
	io, ok := parseZfsKstatIO(zfsFixture(t, "kstat_io.txt"))
	if !ok || io.ReadBytes != 9990000000 || io.WriteBytes != 7020000000 || io.ReadOps != 1320000 || io.WriteOps != 1740000 {
		t.Fatalf("%v %+v", ok, io)
	}
	for _, bad := range []string{"", "1 2 3\n", "nread nwritten\n1 2\n", "nread nwritten reads writes\nx y z w\n"} {
		if _, ok := parseZfsKstatIO(bad); ok {
			t.Fatalf("%q was taken for good", bad)
		}
	}
}

func TestZFSIOReadsEveryPool(t *testing.T) {
	zfsReset(t)
	proc := t.TempDir()
	for _, pool := range []string{"tank", "pool1"} {
		if err := os.MkdirAll(filepath.Join(proc, pool), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(proc, "pool1", "io"), []byte(zfsFixture(t, "kstat_io.txt")), 0o644); err != nil {
		t.Fatal(err)
	}
	zfsNow = func() time.Time { return time.UnixMilli(1762309808123) }
	got := ZFSIO(proc)
	if len(got) != 1 || got[0].Pool != "pool1" || got[0].At != 1762309808123 || got[0].ReadOps != 1320000 {
		t.Fatalf("a pool with no counters is left out, not shown at zero: %+v", got)
	}
	if got := ZFSIO(filepath.Join(proc, "nowhere")); got == nil || len(got) != 0 {
		t.Fatalf("%+v", got)
	}
}

// fakeZFS answers the commands with the fixtures, keyed by "<tool> <subcommand>", and fails the ones in fail. A snapshot listing is keyed "zfs snapshot".
type fakeZFS struct {
	calls atomic.Int32
	seen  []string
	out   map[string]string
	fail  map[string]error
}

func healthyFake(t *testing.T) *fakeZFS {
	t.Helper()
	zfsReset(t)
	f := &fakeZFS{
		out: map[string]string{
			"zpool list":   zfsFixture(t, "zpool_list_one.txt"),
			"zpool status": zfsFixture(t, "zpool_status_healthy_mirror.txt"),
			"zfs list":     zfsFixture(t, "zfs_list_datasets.txt"),
			"zfs snapshot": zfsFixture(t, "zfs_list_snapshots.txt"),
		},
		fail: map[string]error{},
	}
	zfsHas = func() bool { return true }
	zfsExec = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		f.calls.Add(1)
		f.seen = append(f.seen, name+" "+strings.Join(args, " "))
		key := name + " " + args[0]
		if name == "zfs" && strings.Contains(strings.Join(args, " "), "-t snapshot") {
			key = "zfs snapshot"
		}
		if err := f.fail[key]; err != nil {
			return nil, err
		}
		return []byte(f.out[key]), nil
	}
	return f
}

func TestZFSOverview(t *testing.T) {
	f := healthyFake(t)
	state := ZFSOverview()
	if !state.Available || state.Busy || state.Stale || len(state.Pools) != 1 || !state.Pools[0].Known || state.Pools[0].State != "ONLINE" || !state.DatasetsKnown || len(state.Datasets) != 6 {
		t.Fatalf("%+v", state)
	}
	// only these three commands, in the forms that were agreed
	want := []string{
		"zpool list -H -p -o name,size,allocated,free,fragmentation,capacity,health",
		"zpool status -p -P pool1",
		"zfs list -H -p -t filesystem,volume -o name,type,used,available,referenced,usedbysnapshots,mountpoint,mounted,quota,keystatus",
	}
	if strings.Join(f.seen, "\n") != strings.Join(want, "\n") {
		t.Fatalf("%q", f.seen)
	}
	ZFSOverview()
	if f.calls.Load() != 3 {
		t.Fatalf("a second look within the TTL asks nothing more: %d commands", f.calls.Load())
	}
}

func TestZFSOverviewAgainAfterTheTTL(t *testing.T) {
	f := healthyFake(t)
	now := time.Now()
	zfsNow = func() time.Time { return now }
	ZFSOverview()
	now = now.Add(zfsListTTL + time.Second)
	ZFSOverview()
	if f.calls.Load() != 6 {
		t.Fatalf("%d", f.calls.Load())
	}
}

func TestZFSNotThere(t *testing.T) {
	f := healthyFake(t)
	zfsHas = func() bool { return false }
	state := ZFSOverview()
	if state.Available || state.Reason != "tools_missing" || f.calls.Load() != 0 || state.Pools == nil || state.Datasets == nil {
		t.Fatalf("%+v", state)
	}
}

func TestZFSModuleNotLoaded(t *testing.T) {
	f := healthyFake(t)
	f.fail["zpool list"] = errors.New("zpool: exit status 1: The ZFS modules are not loaded.\nTry running '/sbin/modprobe zfs' as root to load them.")
	state := ZFSOverview()
	if state.Available || state.Reason != "module_missing" {
		t.Fatalf("%+v", state)
	}
}

func TestZFSNoPool(t *testing.T) {
	f := healthyFake(t)
	f.out["zpool list"] = zfsFixture(t, "zpool_list_none.txt")
	state := ZFSOverview()
	if !state.Available || len(state.Pools) != 0 || f.calls.Load() != 1 {
		t.Fatalf("%+v after %d commands", state, f.calls.Load())
	}
}

func TestZFSPoolWithNoDatasetsOfItsOwn(t *testing.T) {
	f := healthyFake(t)
	f.out["zfs list"] = zfsFixture(t, "zfs_list_root_only.txt")
	state := ZFSOverview()
	if !state.DatasetsKnown || len(state.Datasets) != 1 {
		t.Fatalf("%+v", state)
	}
	zfsClearCache()
	f.out["zfs list"] = zfsFixture(t, "zfs_list_none.txt")
	state = ZFSOverview()
	if !state.DatasetsKnown || len(state.Datasets) != 0 {
		t.Fatalf("%+v", state)
	}
}

func TestZFSDegradedPoolIsTold(t *testing.T) {
	f := healthyFake(t)
	f.out["zpool list"] = zfsFixture(t, "zpool_list_two.txt")
	f.out["zpool status"] = zfsFixture(t, "zpool_status_degraded_raidz.txt")
	state := ZFSOverview()
	if len(state.Pools) != 2 || state.Pools[1].State != "DEGRADED" || !state.Pools[1].Known {
		t.Fatalf("%+v", state.Pools)
	}
}

func TestZFSUnreadableStatusIsNotHealthy(t *testing.T) {
	f := healthyFake(t)
	f.fail["zpool status"] = errors.New("zpool: signal: killed")
	p := ZFSOverview().Pools[0]
	if p.Known || p.State != "" || p.Size != 1992864825344 || p.Health != "ONLINE" {
		t.Fatalf("the figures stay, the state is not claimed: %+v", p)
	}
	zfsClearCache()
	f.fail["zfs list"] = errors.New("zfs: timeout")
	state := ZFSOverview()
	if state.DatasetsKnown || len(state.Datasets) != 0 {
		t.Fatalf("%+v", state)
	}
}

func TestZFSFailureKeepsTheLastAnswerMarkedStale(t *testing.T) {
	f := healthyFake(t)
	now := time.Now()
	zfsNow = func() time.Time { return now }
	ZFSOverview()
	f.fail["zpool list"] = errors.New("zpool: exit status 1: something broke")
	now = now.Add(zfsListTTL + time.Second)
	state := ZFSOverview()
	if !state.Available || len(state.Pools) != 1 || !state.Stale {
		t.Fatalf("%+v", state)
	}
	// with nothing to fall back on it is busy, which the dashboard shows as "not yet", not as "no ZFS"
	zfsClearCache()
	state = ZFSOverview()
	if !state.Available || !state.Busy {
		t.Fatalf("%+v", state)
	}
}

func TestZFSSnapshotsOnlyOfDatasetsTheBoxLists(t *testing.T) {
	f := healthyFake(t)
	for _, name := range []string{"nope", "pool1/zzz", "-r", "pool1/appdata; reboot", ""} {
		if _, err := ZFSSnapshotsOf(name); !errors.Is(err, ErrZFSNoDataset) {
			t.Fatalf("%q: %v", name, err)
		}
	}
	if f.calls.Load() != 3 {
		t.Fatalf("the overview only, and no snapshot command for a name that is not listed: %d commands", f.calls.Load())
	}
}

func TestZFSSnapshotsOfADataset(t *testing.T) {
	f := healthyFake(t)
	list, err := ZFSSnapshotsOf("pool1/appdata")
	if err != nil || list.Dataset != "pool1/appdata" || len(list.Snapshots) != 2 || list.Truncated {
		t.Fatalf("%+v %v", list, err)
	}
	if last := f.seen[len(f.seen)-1]; last != "zfs list -H -p -t snapshot -d 1 -S creation -o name,used,referenced,written,creation pool1/appdata" {
		t.Fatalf("%q", last)
	}
}

func TestZFSSnapshotsAreCutAtTheLimit(t *testing.T) {
	f := healthyFake(t)
	f.out["zfs snapshot"] = strings.Repeat("pool1/appdata@s\t1\t2\t3\t4\n", ZFSSnapshotLimit+50)
	list, err := ZFSSnapshotsOf("pool1/appdata")
	if err != nil || len(list.Snapshots) != ZFSSnapshotLimit || !list.Truncated {
		t.Fatalf("%d %v %v", len(list.Snapshots), list.Truncated, err)
	}
}

func TestZFSSnapshotsWithoutZFS(t *testing.T) {
	healthyFake(t)
	zfsHas = func() bool { return false }
	if _, err := ZFSSnapshotsOf("pool1"); !errors.Is(err, ErrZFSUnavailable) {
		t.Fatal(err)
	}
}

// A command that does not come back must not be followed by another one.
func TestZFSStuckCommandIsNotPiledUpOn(t *testing.T) {
	zfsReset(t)
	zfsKillGrace = 20 * time.Millisecond
	release := make(chan struct{})
	var started atomic.Int32
	zfsExec = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		started.Add(1)
		<-release // ignores the context, as a process stuck in the kernel does
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := runZFS(ctx, 30*time.Millisecond, "zpool", "status"); err == nil {
		t.Fatal("a command that never came back was taken for done")
	}
	short, cancelShort := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancelShort()
	if _, err := runZFS(short, time.Second, "zpool", "status"); !errors.Is(err, ErrZFSBusy) {
		t.Fatalf("%v", err)
	}
	if started.Load() != 1 {
		t.Fatalf("%d processes", started.Load())
	}
	close(release)
	deadline := time.Now().Add(2 * time.Second)
	for len(zfsSlot) != 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if len(zfsSlot) != 0 {
		t.Fatal("the slot was not given back when the command ended")
	}
}

func TestCappedBufferKeepsTheStart(t *testing.T) {
	b := &cappedBuffer{max: 5}
	n, _ := b.Write([]byte("abc"))
	m, _ := b.Write([]byte("defgh"))
	if n != 3 || m != 5 || b.String() != "abcde" {
		t.Fatalf("%d %d %q", n, m, b.String())
	}
}
