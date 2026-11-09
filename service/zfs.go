package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ZFS, read only: the pools, their datasets and snapshots, as `zpool` and `zfs` print them. Nothing here changes a pool: the commands below are the only ones
// that are ever run, as an argument list (no shell), and the one argument a client gives (a dataset) must be one the box has just listed.
//
// The numbers come from the scripted forms (-H, tab separated, -p, exact bytes), never the forms made for people. `zpool status` has no -H: it is read by its
// section names, and a line that is not understood is skipped.

var (
	// ErrZFSNoDataset: the dataset is not one the box lists.
	ErrZFSNoDataset = errors.New("no such ZFS dataset")
	// ErrZFSUnavailable: ZFS is not usable on this box.
	ErrZFSUnavailable = errors.New("ZFS is not available")
	// ErrZFSBusy: a ZFS command is still running (or stuck), none is started on top of it.
	ErrZFSBusy = errors.New("a ZFS command is still running")
)

const (
	zfsCommandTimeout  = 10 * time.Second
	zfsSnapshotTimeout = 20 * time.Second
	zfsRequestTimeout  = 30 * time.Second
	zfsListTTL         = 10 * time.Second
	zfsMaxOutput       = 4 << 20
	// ZFSSnapshotLimit is how many snapshots of one dataset an answer holds, the newest first.
	ZFSSnapshotLimit = 200
)

// ZFSState is what GET /v1/disks/zfs answers.
type ZFSState struct {
	// Available: ZFS can be used here. Reason says why not when it is false: tools_missing (zpool or zfs not installed), module_missing (installed, not
	// loaded in the kernel) or failed (a command failed otherwise).
	Available bool   `json:"available"`
	Reason    string `json:"reason,omitempty"`
	// Busy: a command is still running, there is nothing to show yet. Stale: this is the last answer that was got, which is older than it should be.
	Busy  bool      `json:"busy,omitempty"`
	Stale bool      `json:"stale,omitempty"`
	Pools []ZFSPool `json:"pools"`
	// DatasetsKnown is false when the datasets could not be read: an empty list then means nothing.
	DatasetsKnown bool         `json:"datasets_known"`
	Datasets      []ZFSDataset `json:"datasets"`
}

// ZFSPool is a pool; all sizes are bytes, -1 stands for "not said".
type ZFSPool struct {
	Name          string `json:"name"`
	Health        string `json:"health"`
	Size          int64  `json:"size"`
	Allocated     int64  `json:"allocated"`
	Free          int64  `json:"free"`
	Fragmentation int    `json:"fragmentation"` // percent
	Capacity      int    `json:"capacity"`      // percent
	// Known: `zpool status` could be read. When it is false only the figures above are good, and the pool must not be shown as healthy.
	Known bool `json:"known"`
	// State is the pool's state in `zpool status`; Status, Action and See are its own words (English, ZFS does not translate them).
	State  string `json:"state"`
	Status string `json:"status,omitempty"`
	Action string `json:"action,omitempty"`
	See    string `json:"see,omitempty"`
	// DataErrors is the number of files with a permanent error, -1 when the line was not read.
	DataErrors int       `json:"data_errors"`
	Scan       ZFSScan   `json:"scan"`
	Vdevs      []ZFSVdev `json:"vdevs"`
}

// ZFSScan is the last scan (scrub or resilver); ZFS keeps one only, a resilver wipes the memory of the scrub before it.
type ZFSScan struct {
	// Kind: none, scrub, resilver or other (Raw says it).
	Kind string `json:"kind"`
	// State: finished, running, paused, canceled, or empty.
	State string `json:"state,omitempty"`
	// Percent and Remaining (seconds) of a scan at work, -1 when not said.
	Percent   float64 `json:"percent"`
	Remaining int64   `json:"remaining"`
	// Repaired is how much was repaired, as ZFS wrote it ("0B").
	Repaired string `json:"repaired,omitempty"`
	Errors   int    `json:"errors"`   // -1 when not said
	Duration int64  `json:"duration"` // seconds, -1 when not said
	// Started and Ended are Unix seconds (the box's local time, which the line does not name), 0 when not said.
	Started int64  `json:"started"`
	Ended   int64  `json:"ended"`
	Raw     string `json:"raw,omitempty"`
}

// ZFSVdev is a line of the pool's tree: a mirror, a raidz, a disk, or a group (logs, cache, spares, special, dedup).
type ZFSVdev struct {
	Name string `json:"name"`
	// Kind: mirror, raidz, draid, replacing, spare, group or disk.
	Kind  string `json:"kind"`
	State string `json:"state,omitempty"`
	Read  int64  `json:"read"` // error counters, -1 when not said
	Write int64  `json:"write"`
	Cksum int64  `json:"cksum"`
	Note  string `json:"note,omitempty"`
	// Disk is the disk a disk vdev is on (sdb, nvme0n1), when it can be told.
	Disk     string    `json:"disk,omitempty"`
	Children []ZFSVdev `json:"children,omitempty"`
}

// ZFSDataset is a filesystem or a volume.
type ZFSDataset struct {
	Name string `json:"name"`
	Pool string `json:"pool"`
	// Type: filesystem or volume.
	Type            string `json:"type"`
	Used            int64  `json:"used"` // with its snapshots and its children
	Available       int64  `json:"available"`
	Referenced      int64  `json:"referenced"`
	UsedBySnapshots int64  `json:"used_by_snapshots"`
	Mountpoint      string `json:"mountpoint"` // a path, none or legacy; empty for a volume
	Mounted         bool   `json:"mounted"`
	Quota           int64  `json:"quota"` // 0: none
	// KeyStatus of an encrypted dataset: available or unavailable; empty when it is not encrypted.
	KeyStatus string `json:"key_status,omitempty"`
}

// ZFSSnapshot is a snapshot of a dataset; Name is the part after the @.
type ZFSSnapshot struct {
	Name       string `json:"name"`
	Used       int64  `json:"used"`
	Referenced int64  `json:"referenced"`
	Written    int64  `json:"written"`
	Created    int64  `json:"created"` // Unix seconds
}

// ZFSSnapshots is the answer to GET /v1/disks/zfs/snapshots.
type ZFSSnapshots struct {
	Dataset   string        `json:"dataset"`
	Snapshots []ZFSSnapshot `json:"snapshots"`
	// Truncated: there are more than ZFSSnapshotLimit; these are the newest.
	Truncated bool `json:"truncated"`
}

// ZFSPoolIO holds the counters of a pool since it was imported; the rate is the difference between two readings over the time between them.
type ZFSPoolIO struct {
	Pool       string `json:"pool"`
	ReadBytes  int64  `json:"read_bytes"`
	WriteBytes int64  `json:"write_bytes"`
	ReadOps    int64  `json:"read_ops"`
	WriteOps   int64  `json:"write_ops"`
	// At is when it was read, Unix milliseconds on the box.
	At int64 `json:"at"`
}

// zfsHas says whether the tools are installed, a var for the tests.
var zfsHas = func() bool {
	for _, tool := range []string{"zpool", "zfs"} {
		if _, err := exec.LookPath(tool); err != nil {
			return false
		}
	}
	return true
}

// zfsExec runs a command and answers its output. A var for the tests. The output is read up to zfsMaxOutput.
var zfsExec = func(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = append(os.Environ(), "LC_ALL=C") // the words of the output are English whatever the language of the box
	cmd.WaitDelay = 2 * time.Second
	out, stderr := &cappedBuffer{max: zfsMaxOutput}, &cappedBuffer{max: 4096}
	cmd.Stdout, cmd.Stderr = out, stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("%s: %w: %s", name, err, strings.TrimSpace(stderr.String()))
	}
	return out.Bytes(), nil
}

// cappedBuffer keeps the first max bytes and drops the rest, so a pool with a million snapshots cannot fill the memory of a small box.
type cappedBuffer struct {
	bytes.Buffer
	max int
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	if room := b.max - b.Len(); room > 0 {
		b.Buffer.Write(p[:min(room, len(p))])
	}
	return len(p), nil
}

// zfsSlot lets one command run at a time. It is given back when the command really ends, not when its caller gives up: a pool that stopped answering can leave
// `zpool status` stuck where it cannot be killed, and a dashboard that asks every few seconds must not pile such processes up.
var zfsSlot = make(chan struct{}, 1)

// zfsKillGrace is how long a command that was told to stop is waited for, a var for the tests.
var zfsKillGrace = 3 * time.Second

func runZFS(ctx context.Context, timeout time.Duration, name string, args ...string) ([]byte, error) {
	select {
	case zfsSlot <- struct{}{}:
	case <-ctx.Done():
		return nil, ErrZFSBusy
	}
	cmdCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	type result struct {
		out []byte
		err error
	}
	done := make(chan result, 1)
	go func() {
		out, err := zfsExec(cmdCtx, name, args...)
		<-zfsSlot
		done <- result{out, err}
	}()
	select {
	case r := <-done:
		return r.out, r.err
	case <-cmdCtx.Done():
		// the process gets its kill and a moment to go; if it does not, we stop waiting and the slot stays taken until it does
		select {
		case r := <-done:
			if r.err == nil {
				return r.out, nil
			}
		case <-time.After(zfsKillGrace):
		}
		return nil, fmt.Errorf("%s: %w", name, cmdCtx.Err())
	}
}

var (
	zfsCache struct {
		sync.Mutex
		state  *ZFSState
		at     time.Time
		flight chan struct{}
	}
	zfsNow = time.Now
)

// ZFSOverview is what the dashboard shows: the pools and the datasets. It is kept for zfsListTTL, and shared by whoever asks meanwhile; when the commands
// cannot be run (one is still stuck), the last answer is given back marked stale, or Busy if there is none.
func ZFSOverview() ZFSState {
	c := &zfsCache
	c.Lock()
	if c.state != nil && zfsNow().Sub(c.at) < zfsListTTL {
		s := *c.state
		c.Unlock()
		return s
	}
	if c.flight != nil {
		flight := c.flight
		c.Unlock()
		select {
		case <-flight:
		case <-time.After(zfsRequestTimeout):
		}
		c.Lock()
		defer c.Unlock()
		return zfsLastKnown()
	}
	c.flight = make(chan struct{})
	flight := c.flight
	c.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), zfsRequestTimeout)
	state := gatherZFS(ctx)
	cancel()

	c.Lock()
	defer c.Unlock()
	if state.Busy || state.Reason == "failed" {
		state = zfsLastKnown()
	}
	s := state
	c.state, c.at = &s, zfsNow()
	c.flight = nil
	close(flight)
	return state
}

// zfsLastKnown is the last good answer, marked stale; call it with the cache locked.
func zfsLastKnown() ZFSState {
	if zfsCache.state == nil || zfsCache.state.Busy {
		return ZFSState{Available: true, Busy: true, Pools: []ZFSPool{}, Datasets: []ZFSDataset{}}
	}
	s := *zfsCache.state
	s.Stale = zfsNow().Sub(zfsCache.at) >= zfsListTTL
	return s
}

func gatherZFS(ctx context.Context) ZFSState {
	state := ZFSState{Pools: []ZFSPool{}, Datasets: []ZFSDataset{}}
	if !zfsHas() {
		state.Reason = "tools_missing"
		return state
	}
	out, err := runZFS(ctx, zfsCommandTimeout, "zpool", "list", "-H", "-p", "-o", "name,size,allocated,free,fragmentation,capacity,health")
	switch {
	case errors.Is(err, ErrZFSBusy):
		state.Available, state.Busy = true, true
		return state
	case err != nil && (strings.Contains(err.Error(), "modules are not loaded") || strings.Contains(err.Error(), "/dev/zfs")):
		state.Reason = "module_missing"
		return state
	case err != nil:
		state.Reason = "failed"
		return state
	}
	state.Available = true
	state.Pools = parseZpoolList(string(out))
	if len(state.Pools) == 0 {
		return state
	}
	for i := range state.Pools {
		pool := &state.Pools[i]
		text, err := runZFS(ctx, zfsCommandTimeout, "zpool", "status", "-p", "-P", pool.Name)
		if err != nil {
			continue // Known stays false: the pool is not shown as well
		}
		parseZpoolStatus(string(text), pool)
	}
	text, err := runZFS(ctx, zfsCommandTimeout, "zfs", "list", "-H", "-p", "-t", "filesystem,volume", "-o", "name,type,used,available,referenced,usedbysnapshots,mountpoint,mounted,quota,keystatus")
	if err == nil {
		state.DatasetsKnown = true
		state.Datasets = parseZfsList(string(text))
	}
	return state
}

// ZFSSnapshotsOf lists the snapshots of one dataset, the newest first. The dataset must be one the box lists.
func ZFSSnapshotsOf(dataset string) (ZFSSnapshots, error) {
	overview := ZFSOverview()
	if !overview.Available {
		return ZFSSnapshots{}, ErrZFSUnavailable
	}
	if overview.Busy {
		return ZFSSnapshots{}, ErrZFSBusy
	}
	known := false
	for _, d := range overview.Datasets {
		known = known || d.Name == dataset
	}
	if !known || strings.HasPrefix(dataset, "-") {
		return ZFSSnapshots{}, ErrZFSNoDataset
	}
	ctx, cancel := context.WithTimeout(context.Background(), zfsRequestTimeout)
	defer cancel()
	out, err := runZFS(ctx, zfsSnapshotTimeout, "zfs", "list", "-H", "-p", "-t", "snapshot", "-d", "1", "-S", "creation", "-o", "name,used,referenced,written,creation", dataset)
	if errors.Is(err, ErrZFSBusy) {
		return ZFSSnapshots{}, ErrZFSBusy
	}
	if err != nil {
		return ZFSSnapshots{}, err
	}
	list := ZFSSnapshots{Dataset: dataset, Snapshots: parseZfsSnapshots(string(out))}
	if len(list.Snapshots) > ZFSSnapshotLimit {
		list.Snapshots, list.Truncated = list.Snapshots[:ZFSSnapshotLimit], true
	}
	return list, nil
}

// ZFSIO reads the counters of every pool under the kstat folder at proc ("/proc/spl/kstat/zfs"). No command is run; a pool with no such file is left out.
func ZFSIO(proc string) []ZFSPoolIO {
	files, _ := filepath.Glob(filepath.Join(proc, "*", "io"))
	sort.Strings(files)
	now := zfsNow().UnixMilli()
	list := []ZFSPoolIO{}
	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			continue
		}
		if io, ok := parseZfsKstatIO(string(data)); ok {
			io.Pool, io.At = filepath.Base(filepath.Dir(file)), now
			list = append(list, io)
		}
	}
	return list
}

// --- parsers: text in, values out, nothing is run ---

func zfsInt(s string) int64 {
	n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil {
		return -1 // "-" is how ZFS says a value does not apply
	}
	return n
}

// parseZpoolList reads `zpool list -H -p -o name,size,allocated,free,fragmentation,capacity,health`. A line without all the fields ("no pools available") is skipped.
func parseZpoolList(text string) []ZFSPool {
	pools := []ZFSPool{}
	for _, line := range strings.Split(text, "\n") {
		f := strings.Split(strings.TrimRight(line, "\r"), "\t")
		if len(f) < 7 || f[0] == "" {
			continue
		}
		pools = append(pools, ZFSPool{
			Name: f[0], Size: zfsInt(f[1]), Allocated: zfsInt(f[2]), Free: zfsInt(f[3]),
			Fragmentation: int(zfsInt(f[4])), Capacity: int(zfsInt(f[5])), Health: f[6],
			DataErrors: -1, Scan: ZFSScan{Kind: "none", Percent: -1, Remaining: -1, Errors: -1, Duration: -1}, Vdevs: []ZFSVdev{},
		})
	}
	return pools
}

// parseZfsList reads `zfs list -H -p -t filesystem,volume -o name,type,used,available,referenced,usedbysnapshots,mountpoint,mounted,quota,keystatus`.
func parseZfsList(text string) []ZFSDataset {
	datasets := []ZFSDataset{}
	for _, line := range strings.Split(text, "\n") {
		f := strings.Split(strings.TrimRight(line, "\r"), "\t")
		if len(f) < 10 || f[0] == "" {
			continue
		}
		d := ZFSDataset{
			Name: f[0], Pool: strings.SplitN(f[0], "/", 2)[0], Type: f[1],
			Used: zfsInt(f[2]), Available: zfsInt(f[3]), Referenced: zfsInt(f[4]), UsedBySnapshots: zfsInt(f[5]),
			Mounted: f[7] == "yes", Quota: max(zfsInt(f[8]), 0),
		}
		if f[6] != "-" {
			d.Mountpoint = f[6]
		}
		if f[9] != "-" {
			d.KeyStatus = f[9]
		}
		datasets = append(datasets, d)
	}
	return datasets
}

// parseZfsSnapshots reads `zfs list -H -p -t snapshot -o name,used,referenced,written,creation`.
func parseZfsSnapshots(text string) []ZFSSnapshot {
	snapshots := []ZFSSnapshot{}
	for _, line := range strings.Split(text, "\n") {
		f := strings.Split(strings.TrimRight(line, "\r"), "\t")
		if len(f) < 5 {
			continue
		}
		_, short, ok := strings.Cut(f[0], "@")
		if !ok {
			continue
		}
		snapshots = append(snapshots, ZFSSnapshot{Name: short, Used: zfsInt(f[1]), Referenced: zfsInt(f[2]), Written: zfsInt(f[3]), Created: max(zfsInt(f[4]), 0)})
	}
	return snapshots
}

// parseZfsKstatIO reads /proc/spl/kstat/zfs/<pool>/io: a header line, a line of column names, a line of values.
func parseZfsKstatIO(text string) (ZFSPoolIO, bool) {
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		names := strings.Fields(line)
		if i+1 >= len(lines) || len(names) == 0 {
			continue
		}
		col := map[string]int{}
		for n, name := range names {
			col[name] = n
		}
		values := strings.Fields(lines[i+1])
		get := func(name string) (int64, bool) {
			n, ok := col[name]
			if !ok || n >= len(values) {
				return 0, false
			}
			v, err := strconv.ParseInt(values[n], 10, 64)
			return v, err == nil
		}
		nread, ok1 := get("nread")
		nwritten, ok2 := get("nwritten")
		reads, ok3 := get("reads")
		writes, ok4 := get("writes")
		if ok1 && ok2 && ok3 && ok4 {
			return ZFSPoolIO{ReadBytes: nread, WriteBytes: nwritten, ReadOps: reads, WriteOps: writes}, true
		}
	}
	return ZFSPoolIO{}, false
}

var zfsSection = regexp.MustCompile(`^ {0,3}(pool|state|status|action|see|scan|config|errors):\s?(.*)$`)

// parseZpoolStatus fills the pool with what `zpool status -p -P <pool>` says. It never fails: what is not understood is left as it was, and Known is set only
// when the state was read.
func parseZpoolStatus(text string, pool *ZFSPool) {
	sections := map[string][]string{}
	var key string
	var config []string
	inConfig := false
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimRight(line, "\r")
		if m := zfsSection.FindStringSubmatch(line); m != nil {
			key = m[1]
			inConfig = key == "config"
			if !inConfig {
				sections[key] = append(sections[key], strings.TrimSpace(m[2]))
			}
			continue
		}
		switch {
		case key == "" || strings.TrimSpace(line) == "":
			// the blank line closes the tree, and is nothing elsewhere
			if inConfig && len(config) > 0 {
				inConfig = false
			}
		case inConfig:
			config = append(config, line)
		default:
			sections[key] = append(sections[key], strings.TrimSpace(line))
		}
	}
	join := func(k string) string { return strings.TrimSpace(strings.Join(sections[k], " ")) }

	pool.State = join("state")
	pool.Known = pool.State != ""
	pool.Status, pool.Action, pool.See = join("status"), join("action"), join("see")
	pool.DataErrors = parseZfsDataErrors(join("errors"))
	pool.Scan = parseZfsScan(sections["scan"])
	pool.Vdevs = parseZfsVdevs(config)
}

var zfsDataErrors = regexp.MustCompile(`^(\d+) data errors?`)

func parseZfsDataErrors(line string) int {
	if strings.HasPrefix(line, "No known data errors") {
		return 0
	}
	if m := zfsDataErrors.FindStringSubmatch(line); m != nil {
		n, _ := strconv.Atoi(m[1])
		return n
	}
	return -1
}

var (
	zfsScanDone    = regexp.MustCompile(`^(?:(scrub) repaired|(resilvered)) (\S+) in (.+?) with (\d+) errors? on (.+)$`)
	zfsScanRunning = regexp.MustCompile(`^(scrub|resilver) in progress since (.+)$`)
	zfsScanStopped = regexp.MustCompile(`^(scrub|resilver) (canceled|paused)(?: on| since) (.+)$`)
	zfsScanPercent = regexp.MustCompile(`(\d+(?:\.\d+)?)% done`)
	zfsScanToGo    = regexp.MustCompile(`(\S+) to go`)
	zfsDuration    = regexp.MustCompile(`^(?:(\d+) days? )?(\d+):(\d+):(\d+)$`)
)

// zfsTime reads "Sun Oct 11 00:40:48 2026" in the box's own time zone, which the line does not name.
func zfsTime(s string) int64 {
	t, err := time.ParseInLocation("Mon Jan _2 15:04:05 2006", strings.Join(strings.Fields(s), " "), time.Local)
	if err != nil {
		return 0
	}
	return t.Unix()
}

func zfsDurationSeconds(s string) int64 {
	m := zfsDuration.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return -1
	}
	days, _ := strconv.Atoi(m[1])
	h, _ := strconv.Atoi(m[2])
	mi, _ := strconv.Atoi(m[3])
	sec, _ := strconv.Atoi(m[4])
	return int64(((days*24+h)*60+mi)*60 + sec)
}

func parseZfsScan(lines []string) ZFSScan {
	scan := ZFSScan{Kind: "none", Percent: -1, Remaining: -1, Errors: -1, Duration: -1}
	if len(lines) == 0 || lines[0] == "" {
		return scan
	}
	scan.Raw = strings.Join(lines, " ")
	first := lines[0]
	rest := strings.Join(lines[1:], " ")
	switch {
	case strings.HasPrefix(first, "none requested"):
		scan.Kind, scan.Raw = "none", ""
	case zfsScanDone.MatchString(first):
		m := zfsScanDone.FindStringSubmatch(first)
		scan.Kind, scan.State = "scrub", "finished"
		if m[2] != "" {
			scan.Kind = "resilver"
		}
		scan.Repaired, scan.Duration = m[3], zfsDurationSeconds(m[4])
		scan.Errors, _ = strconv.Atoi(m[5])
		scan.Ended = zfsTime(m[6])
	case zfsScanRunning.MatchString(first):
		m := zfsScanRunning.FindStringSubmatch(first)
		scan.Kind, scan.State, scan.Started = m[1], "running", zfsTime(m[2])
		if p := zfsScanPercent.FindStringSubmatch(rest); p != nil {
			scan.Percent, _ = strconv.ParseFloat(p[1], 64)
		}
		if g := zfsScanToGo.FindStringSubmatch(rest); g != nil {
			scan.Remaining = zfsDurationSeconds(g[1])
		}
	case zfsScanStopped.MatchString(first):
		m := zfsScanStopped.FindStringSubmatch(first)
		scan.Kind, scan.State = m[1], m[2]
		if m[2] == "canceled" {
			scan.Ended = zfsTime(m[3])
		} else {
			scan.Started = zfsTime(m[3])
		}
	default:
		scan.Kind = "other"
	}
	return scan
}

var zfsVdevName = regexp.MustCompile(`^(mirror|raidz[123]?|draid[123]?(?::\S+)?|replacing|spare)-\d+$`)

func zfsIsState(s string) bool {
	switch s {
	case "ONLINE", "DEGRADED", "FAULTED", "OFFLINE", "UNAVAIL", "REMOVED", "SUSPENDED", "AVAIL", "INUSE":
		return true
	}
	return false
}

// parseZfsVdevs reads the rows of the config: section. The first row is the pool itself and is not returned (its children are); the other rows at the same
// depth (logs, cache, spares...) are groups.
func parseZfsVdevs(rows []string) []ZFSVdev {
	type node struct {
		vdev     ZFSVdev
		children []*node
	}
	var tops []*node
	var stack []*node // stack[i] is the last row seen at depth i
	var root *node
	for _, row := range rows {
		row = strings.TrimLeft(row, "\t")
		trimmed := strings.TrimLeft(row, " ")
		fields := strings.Fields(trimmed)
		if len(fields) == 0 || (fields[0] == "NAME" && len(fields) > 1 && fields[1] == "STATE") {
			continue
		}
		depth := (len(row) - len(trimmed)) / 2
		v := ZFSVdev{Name: fields[0], Read: -1, Write: -1, Cksum: -1}
		if len(fields) >= 2 && zfsIsState(fields[1]) {
			v.State = fields[1]
			if len(fields) >= 5 {
				v.Read, v.Write, v.Cksum = zfsInt(fields[2]), zfsInt(fields[3]), zfsInt(fields[4])
				v.Note = strings.Join(fields[5:], " ")
			}
		}
		m := zfsVdevName.FindStringSubmatch(v.Name)
		switch {
		case m != nil:
			v.Kind = strings.TrimRight(m[1], "0123")
			if strings.HasPrefix(m[1], "draid") {
				v.Kind = "draid"
			}
		case depth == 0 && root != nil:
			v.Kind = "group"
		default:
			v.Kind = "disk"
			if strings.HasPrefix(v.Name, "/dev/") {
				v.Disk = zfsResolveDisk(v.Name)
			}
		}
		n := &node{vdev: v}
		switch {
		case root == nil:
			root = n
			stack = []*node{n} // the pool: its children are the top of the tree
		case depth == 0:
			stack = []*node{n}
			tops = append(tops, n)
		default:
			if depth > len(stack) {
				depth = len(stack)
			}
			if parent := stack[depth-1]; parent == root {
				tops = append(tops, n)
			} else {
				parent.children = append(parent.children, n)
			}
			stack = append(stack[:depth], n)
		}
	}
	var build func(n *node) ZFSVdev
	build = func(n *node) ZFSVdev {
		v := n.vdev
		for _, c := range n.children {
			v.Children = append(v.Children, build(c))
		}
		return v
	}
	out := []ZFSVdev{}
	for _, n := range tops {
		out = append(out, build(n))
	}
	return out
}

// zfsResolveDisk is the disk (sdb, nvme0n1) a device path is on, or empty, a var for the tests.
var zfsResolveDisk = func(path string) string {
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return ""
	}
	return diskOf("/sys", filepath.Base(real))
}
