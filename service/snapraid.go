package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ReCasaOS/CasaOS-LocalStorage/pkg/config"
	"github.com/shirou/gopsutil/v3/disk"
)

// Protecting the pooled disks with SnapRAID: one or two parity disks hold what is needed to rebuild a data disk that dies, and each data disk stays a plain
// filesystem that can be read alone. It is not live like RAID: a file written after the last sync is not protected until the next one (a daily one is scheduled),
// and a sync that would forget a lot of files is held back (snapraid_run.go).

var (
	ErrNoSnapRAID         = errors.New("snapraid is not installed and could not be installed (sudo apt install snapraid)")
	ErrSnapRAIDNotSet     = errors.New("SnapRAID is not set up")
	ErrSnapRAIDBusy       = errors.New("SnapRAID is already at work: wait for it, or stop it")
	ErrSnapRAIDParity     = errors.New("one or two parity disks are needed")
	ErrSnapRAIDData       = errors.New("at least one data disk is needed")
	ErrSnapRAIDNotMounted = errors.New("a disk is not mounted where it should be: SnapRAID would take its files for deleted")
	ErrSnapRAIDSameDisk   = errors.New("a disk is named twice, or two of them are on the same device")
	ErrSnapRAIDParitySize = errors.New("a parity disk has less room than the fullest data disk")
	ErrSnapRAIDAction     = errors.New("no such action: diff, sync, scrub or fix")
	ErrSnapRAIDConsent    = errors.New("fix puts files back as they were at the last sync: say which disk it is, typed again in confirm (or all)")
	ErrSnapRAIDNoDisk     = errors.New("no such data disk")
)

// SnapRAIDDisk is a data disk: the name SnapRAID knows it by (d1, d2: it never changes once given, a new disk takes the next free one) and where it is mounted.
type SnapRAIDDisk struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

// SnapRAIDConfig is what was asked for.
type SnapRAIDConfig struct {
	Data   []SnapRAIDDisk `json:"data"`
	Parity []string       `json:"parity"`
	// MaxRemoved is how many removed files a sync accepts without being told to go on: more is a disk that lost its files, or a person who deleted a folder.
	MaxRemoved int `json:"max_removed"`
	// Hour is when the daily sync runs, 0 to 23; -1 for never (the buttons still work).
	Hour int `json:"hour"`
}

const (
	snapraidDefaultMaxRemoved = 40
	snapraidDefaultHour       = 3
)

// SnapRAIDDiff is what snapraid diff counts.
type SnapRAIDDiff struct {
	Equal    int `json:"equal"`
	Added    int `json:"added"`
	Removed  int `json:"removed"`
	Updated  int `json:"updated"`
	Moved    int `json:"moved"`
	Copied   int `json:"copied"`
	Restored int `json:"restored"`
}

// SnapRAIDResult is how the last run of an action ended.
type SnapRAIDResult struct {
	At      time.Time `json:"at"`
	OK      bool      `json:"ok"`
	Blocked bool      `json:"blocked,omitempty"`
	// Message is why it did not go well, in snapraid's own words or ours.
	Message string        `json:"message,omitempty"`
	Diff    *SnapRAIDDiff `json:"diff,omitempty"`
}

// SnapRAIDJob is the action at work.
type SnapRAIDJob struct {
	Action    string    `json:"action"`
	StartedAt time.Time `json:"started_at"`
	Percent   int       `json:"percent"`
}

// SnapRAIDState is what the dashboard shows.
type SnapRAIDState struct {
	Installed  bool                      `json:"installed"`
	Configured bool                      `json:"configured"`
	Config     *SnapRAIDConfig           `json:"config,omitempty"`
	Job        *SnapRAIDJob              `json:"job,omitempty"`
	Last       map[string]SnapRAIDResult `json:"last"`
	// Missing are the disks that are not mounted where they were set up.
	Missing []string `json:"missing,omitempty"`
}

type snapraidFile struct {
	Config SnapRAIDConfig            `json:"config"`
	Last   map[string]SnapRAIDResult `json:"last"`
}

// Seams for the tests.
var (
	snapraidDir = func() string { return config.AppInfo.DBPath }
	snapraidNow = time.Now
	hasSnapRAID = func() bool { _, err := exec.LookPath("snapraid"); return err == nil }
	// snapraidMounts answers, for each mount point, the device it is on ("major:minor").
	snapraidMounts = readMountDevices
	// snapraidUsage answers the room of the filesystem at path.
	snapraidUsage = func(path string) (total, free, used uint64, err error) {
		u, err := disk.Usage(path)
		if err != nil {
			return 0, 0, 0, err
		}
		return u.Total, u.Free, u.Used, nil
	}
)

var snapraidMu sync.Mutex // the file, the job

func snapraidStatePath() string { return filepath.Join(snapraidDir(), "snapraid.json") }
func snapraidConfPath() string  { return filepath.Join(snapraidDir(), "snapraid.conf") }

func loadSnapRAID() (snapraidFile, bool) {
	var f snapraidFile
	data, err := os.ReadFile(snapraidStatePath())
	if err != nil || json.Unmarshal(data, &f) != nil || len(f.Config.Parity) == 0 {
		return snapraidFile{Last: map[string]SnapRAIDResult{}}, false
	}
	if f.Last == nil {
		f.Last = map[string]SnapRAIDResult{}
	}
	return f, true
}

func saveSnapRAID(f snapraidFile) error {
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(snapraidDir(), 0o755); err != nil {
		return err
	}
	// written beside, then moved: a crash leaves the old file, never half of the new one
	tmp := snapraidStatePath() + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, snapraidStatePath())
}

// readMountDevices reads the mount points of this box and the device of each from /proc/self/mountinfo.
func readMountDevices() (map[string]string, error) {
	data, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return nil, err
	}
	return parseMountInfo(string(data)), nil
}

// parseMountInfo: "36 35 98:0 /root /mnt/point opts ... - fstype source opts"; a space in a path is written \040.
func parseMountInfo(text string) map[string]string {
	mounts := map[string]string{}
	unescape := strings.NewReplacer(`\040`, " ", `\011`, "\t", `\012`, "\n", `\134`, `\`)
	for _, line := range strings.Split(text, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 5 {
			continue
		}
		mounts[unescape.Replace(fields[4])] = fields[2]
	}
	return mounts
}

// CheckSnapRAIDDisks says whether these disks can be protected as asked. Every one is a mount point of its own (a folder of the system disk would have no
// failure of its own to survive), no two share a device, and each parity disk has the room the fullest data disk takes.
func CheckSnapRAIDDisks(data, parity []string) error {
	if len(parity) < 1 || len(parity) > 2 {
		return ErrSnapRAIDParity
	}
	if len(data) < 1 {
		return ErrSnapRAIDData
	}
	mounts, err := snapraidMounts()
	if err != nil {
		return err
	}
	devices := map[string]bool{}
	var mostUsed uint64
	for i, path := range append(append([]string{}, data...), parity...) {
		path = filepath.Clean(path)
		device, ok := mounts[path]
		if !ok {
			return fmt.Errorf("%w: %s", ErrSnapRAIDNotMounted, path)
		}
		if devices[device] {
			return fmt.Errorf("%w: %s", ErrSnapRAIDSameDisk, path)
		}
		devices[device] = true
		if i < len(data) {
			if _, _, used, err := snapraidUsage(path); err == nil && used > mostUsed {
				mostUsed = used
			}
		}
	}
	for _, path := range parity {
		if _, free, _, err := snapraidUsage(filepath.Clean(path)); err == nil && free < mostUsed {
			return fmt.Errorf("%w: %s", ErrSnapRAIDParitySize, path)
		}
	}
	return nil
}

// nameSnapRAIDDisks gives each path the name it already had, or the next free dN.
func nameSnapRAIDDisks(old []SnapRAIDDisk, paths []string) []SnapRAIDDisk {
	named := map[string]string{}
	taken := map[string]bool{}
	for _, d := range old {
		named[d.Path] = d.Name
	}
	for _, path := range paths {
		if name, ok := named[path]; ok {
			taken[name] = true
		}
	}
	next := 1
	disks := make([]SnapRAIDDisk, 0, len(paths))
	for _, path := range paths {
		name, ok := named[path]
		if !ok {
			for taken["d"+strconv.Itoa(next)] || snapraidNameWas(old, "d"+strconv.Itoa(next)) {
				next++
			}
			name = "d" + strconv.Itoa(next)
			taken[name] = true
		}
		disks = append(disks, SnapRAIDDisk{Name: name, Path: path})
	}
	return disks
}

// snapraidNameWas: a name given once is not given again to another disk, even after the first one left.
func snapraidNameWas(old []SnapRAIDDisk, name string) bool {
	for _, d := range old {
		if d.Name == name {
			return true
		}
	}
	return false
}

// SetSnapRAID checks the disks, installs snapraid if the box lacks it, and keeps the setup. maxRemoved and hour are the defaults when nil.
func SetSnapRAID(data, parity []string, maxRemoved, hour *int) (SnapRAIDConfig, error) {
	cleaned := func(paths []string) []string {
		out := make([]string, 0, len(paths))
		for _, p := range paths {
			out = append(out, filepath.Clean(p))
		}
		return out
	}
	data, parity = cleaned(data), cleaned(parity)
	if err := CheckSnapRAIDDisks(data, parity); err != nil {
		return SnapRAIDConfig{}, err
	}
	if err := ensureSnapRAID(); err != nil {
		return SnapRAIDConfig{}, err
	}

	snapraidMu.Lock()
	defer snapraidMu.Unlock()
	if snapraidJob != nil {
		return SnapRAIDConfig{}, ErrSnapRAIDBusy
	}
	f, _ := loadSnapRAID()
	cfg := SnapRAIDConfig{Data: nameSnapRAIDDisks(f.Config.Data, data), Parity: parity, MaxRemoved: snapraidDefaultMaxRemoved, Hour: snapraidDefaultHour}
	if maxRemoved != nil && *maxRemoved >= 0 {
		cfg.MaxRemoved = *maxRemoved
	}
	if hour != nil && *hour >= -1 && *hour <= 23 {
		cfg.Hour = *hour
	}
	f.Config = cfg
	if err := writeSnapRAIDConf(cfg); err != nil {
		return SnapRAIDConfig{}, err
	}
	return cfg, saveSnapRAID(f)
}

// ClearSnapRAID forgets the setup. The parity files stay where they are, on the parity disks: they cost room, and they are what a later setup of the same
// disks would take up again.
func ClearSnapRAID() error {
	snapraidMu.Lock()
	defer snapraidMu.Unlock()
	if snapraidJob != nil {
		return ErrSnapRAIDBusy
	}
	for _, path := range []string{snapraidStatePath(), snapraidConfPath()} {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

func ensureSnapRAID() error {
	if hasSnapRAID() {
		return nil
	}
	if err := raidInstall("snapraid"); err != nil {
		return fmt.Errorf("%w: %v", ErrNoSnapRAID, err)
	}
	if !hasSnapRAID() {
		return ErrNoSnapRAID
	}
	return nil
}

// snapraidConfText is snapraid.conf for cfg. The content files (the list of what was protected) are on the system disk and on each parity disk: SnapRAID wants
// two on different disks at least, and none of them on a data disk, where the loss of that disk would take its list with it.
func snapraidConfText(cfg SnapRAIDConfig) string {
	var b strings.Builder
	b.WriteString("# written by CasaOS-LocalStorage at each change: do not edit it\n")
	for i, path := range cfg.Parity {
		if i == 0 {
			fmt.Fprintf(&b, "parity %s\n", filepath.Join(path, "snapraid.parity"))
		} else {
			fmt.Fprintf(&b, "%d-parity %s\n", i+1, filepath.Join(path, "snapraid.parity"))
		}
	}
	fmt.Fprintf(&b, "content %s\n", filepath.Join(snapraidDir(), "snapraid.content"))
	for _, path := range cfg.Parity {
		fmt.Fprintf(&b, "content %s\n", filepath.Join(path, "snapraid.content"))
	}
	for _, d := range cfg.Data {
		fmt.Fprintf(&b, "data %s %s/\n", d.Name, strings.TrimSuffix(d.Path, "/"))
	}
	b.WriteString("exclude /lost+found/\nexclude *.unrecoverable\n")
	return b.String()
}

func writeSnapRAIDConf(cfg SnapRAIDConfig) error {
	if err := os.MkdirAll(snapraidDir(), 0o755); err != nil {
		return err
	}
	return os.WriteFile(snapraidConfPath(), []byte(snapraidConfText(cfg)), 0o600)
}

// missingSnapRAIDDisks are the disks of cfg that are not a mount point now.
func missingSnapRAIDDisks(cfg SnapRAIDConfig) []string {
	mounts, err := snapraidMounts()
	if err != nil {
		return nil
	}
	var missing []string
	for _, d := range cfg.Data {
		if _, ok := mounts[d.Path]; !ok {
			missing = append(missing, d.Name)
		}
	}
	for i, path := range cfg.Parity {
		if _, ok := mounts[path]; !ok {
			missing = append(missing, "parity"+strconv.Itoa(i+1))
		}
	}
	sort.Strings(missing)
	return missing
}

// SnapRAIDStatus is what the dashboard shows now.
func SnapRAIDStatus() SnapRAIDState {
	snapraidMu.Lock()
	defer snapraidMu.Unlock()
	f, ok := loadSnapRAID()
	state := SnapRAIDState{Installed: hasSnapRAID(), Configured: ok, Last: f.Last}
	if snapraidJob != nil {
		job := *snapraidJob
		state.Job = &job
	}
	if ok {
		cfg := f.Config
		state.Config = &cfg
		state.Missing = missingSnapRAIDDisks(cfg)
	}
	return state
}
