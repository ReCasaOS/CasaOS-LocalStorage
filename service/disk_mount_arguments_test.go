package service

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ReCasaOS/CasaOS-Common/utils/logger"
	"github.com/ReCasaOS/CasaOS-LocalStorage/pkg/config"
)

// The device and the mount point reach the helper as two arguments, as they are.
// The mount point is made of what the disk says about itself, its label and its
// model: a space in it does not make two arguments, and a command in it is not run.
func TestMountDiskHandsTheHelperItsTwoArgumentsAsTheyAre(t *testing.T) {
	logger.LogInitConsoleOnly()
	shell := t.TempDir()
	seen := filepath.Join(shell, "seen")
	helper := `do_mount() { echo "$#" > "` + seen + `"; echo "$1" >> "` + seen + `"; echo "$2" >> "` + seen + `"; }` + "\n"
	if err := os.WriteFile(filepath.Join(shell, "local-storage-helper.sh"), []byte(helper), 0o600); err != nil {
		t.Fatal(err)
	}
	previous := config.AppInfo.ShellPath
	config.AppInfo.ShellPath = shell
	t.Cleanup(func() { config.AppInfo.ShellPath = previous })

	work := t.TempDir()
	t.Chdir(work) // where a command slipped into the mount point would leave its mark
	mountPoint := filepath.Join(work, "Storage_sdz1_My Passport_$(touch pwned);touch pwned")

	if out, err := (&diskService{}).MountDisk("/dev/sdz1", mountPoint); err != nil {
		t.Fatalf("%v: %s", err, out)
	}

	got, err := os.ReadFile(seen)
	if err != nil {
		t.Fatal(err)
	}
	if want := "2\n/dev/sdz1\n" + mountPoint + "\n"; string(got) != want {
		t.Errorf("the helper was given %q, want %q", got, want)
	}
	if _, err := os.Stat(filepath.Join(work, "pwned")); err == nil {
		t.Error("a command in the mount point was run")
	}
}

// fakeTool puts a shell script named name ahead of the real tool in PATH.
func fakeTool(t *testing.T, bin, name, script string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\n"+script+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
}

// The helper asks the disk for its filesystem type and takes the answer as a
// value. Nothing a disk says about itself is run: not its label, not the name
// of its partition.
func TestTheHelperRunsNothingTheDiskSaysAboutItself(t *testing.T) {
	logger.LogInitConsoleOnly()
	shell, err := filepath.Abs("../build/sysroot/usr/share/casaos/shell") // the script that ships
	if err != nil {
		t.Fatal(err)
	}
	previous := config.AppInfo.ShellPath
	config.AppInfo.ShellPath = shell
	t.Cleanup(func() { config.AppInfo.ShellPath = previous })

	bin, work := t.TempDir(), t.TempDir()
	mounted := filepath.Join(work, "mounted")
	fakeTool(t, bin, "lsblk", "exit 0") // not mounted yet
	fakeTool(t, bin, "blkid", `case "$*" in
*udev*) echo 'ID_FS_PARTLABEL=x;touch pwned;ID_FS_LABEL'; echo 'ID_FS_TYPE=ext4' ;;
*) echo ext4 ;;
esac`)
	fakeTool(t, bin, "mount", `echo "$@" > "`+mounted+`"`)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Chdir(work) // where a command of the disk would leave its mark

	mountPoint := filepath.Join(work, "Storage_sdz1_My Passport")
	if out, err := (&diskService{}).MountDisk("/dev/sdz1", mountPoint); err != nil {
		t.Fatalf("%v: %s", err, out)
	}

	if _, err := os.Stat(filepath.Join(work, "pwned")); err == nil {
		t.Error("the name of the partition was run")
	}
	got, err := os.ReadFile(mounted)
	if err != nil {
		t.Fatal(err)
	}
	if want := "-o noatime /dev/sdz1 " + mountPoint + "\n"; string(got) != want {
		t.Errorf("mount was given %q, want %q", got, want)
	}
}
