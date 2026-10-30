package service

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/ReCasaOS/CasaOS-LocalStorage/model"
)

// Making a software RAID array out of blank disks, with mdadm. The levels offered are those that go on when a disk fails.

var (
	ErrRAIDLevel       = errors.New("this RAID level is not offered")
	ErrRAIDTooFewDisks = errors.New("too few disks for this RAID level")
	ErrRAIDSameDisk    = errors.New("a disk is named twice")
)

// RAIDLevels is the least number of disks an array of each level is made of. raid0 and linear are not here: one failed disk loses everything on them.
var RAIDLevels = map[string]int{"raid1": 2, "raid5": 3, "raid6": 4, "raid10": 4}

// CreateRAIDArray wipes the disks (what RAIDCandidate gave) and makes one array of them at level, under the first mdN that arrays does not hold. It answers the array's
// path (/dev/md0); the array starts to synchronize at once and can be used meanwhile.
func CreateRAIDArray(arrays []RAIDArray, level string, disks []model.LSBLKModel) (string, error) {
	least, ok := RAIDLevels[level]
	if !ok {
		return "", ErrRAIDLevel
	}
	if len(disks) < least {
		return "", fmt.Errorf("%w: %s needs %d", ErrRAIDTooFewDisks, level, least)
	}
	seen := map[string]bool{}
	for _, d := range disks {
		if seen[d.Path] {
			return "", ErrRAIDSameDisk
		}
		seen[d.Path] = true
	}
	if !hasMDADM() {
		return "", ErrNoMDADM
	}
	for _, d := range disks {
		if err := wipeRAIDDisk(d); err != nil {
			return "", err
		}
	}

	path := "/dev/" + nextRAIDName(arrays)
	// --run: mdadm asks nothing, and there is nobody to answer
	args := []string{"--create", path, "--run", "--metadata=1.2", "--level=" + strings.TrimPrefix(level, "raid"), "--raid-devices=" + strconv.Itoa(len(disks))}
	for _, d := range disks {
		args = append(args, d.Path)
	}
	if err := raidRun("mdadm", args...); err != nil {
		return "", fmt.Errorf("making %s: %w", path, err)
	}
	// the node is there at once; udev may still be reading the new disks
	_ = raidRun("udevadm", "settle")
	return path, nil
}

// nextRAIDName is the first mdN that no array of arrays has.
func nextRAIDName(arrays []RAIDArray) string {
	taken := map[string]bool{}
	for _, a := range arrays {
		taken[a.Name] = true
	}
	for i := 0; ; i++ {
		if name := "md" + strconv.Itoa(i); !taken[name] {
			return name
		}
	}
}
