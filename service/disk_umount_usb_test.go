package service

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/ReCasaOS/CasaOS-Common/utils/logger"
	"github.com/ReCasaOS/CasaOS-LocalStorage/pkg/config"
)

// Ejecting a USB drive ran "source" as if it were a program, which no box has: it
// failed every time. The helper is read by bash, and gets the mount point as it is.
func TestUmountUSBHandsTheHelperTheMountPointAsItIs(t *testing.T) {
	logger.LogInitConsoleOnly()
	shell := t.TempDir()
	seen := filepath.Join(shell, "seen")
	helper := `UDEVILUmount() { echo "$#" > "` + seen + `"; echo "$1" >> "` + seen + `"; }` + "\n"
	if err := os.WriteFile(filepath.Join(shell, "local-storage-helper.sh"), []byte(helper), 0o600); err != nil {
		t.Fatal(err)
	}
	previous := config.AppInfo.ShellPath
	config.AppInfo.ShellPath = shell
	t.Cleanup(func() { config.AppInfo.ShellPath = previous })

	work := t.TempDir()
	t.Chdir(work) // where a command slipped into the mount point would leave its mark
	mountPoint := filepath.Join(work, "My Passport;touch pwned")

	if err := (&diskService{}).UmountUSB(mountPoint); err != nil {
		t.Fatal(err)
	}

	got, err := os.ReadFile(seen)
	if err != nil {
		t.Fatal(err)
	}
	if want := "1\n" + mountPoint + "\n"; string(got) != want {
		t.Errorf("the helper was given %q, want %q", got, want)
	}
	if _, err := os.Stat(filepath.Join(work, "pwned")); err == nil {
		t.Error("a command in the mount point was run")
	}
}

// The helper that ships hands the mount point on to udevil whole, spaces and all.
func TestTheHelperEjectsAMountPointWithASpaceInIt(t *testing.T) {
	shell, err := filepath.Abs("../build/sysroot/usr/share/casaos/shell") // the script that ships
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	seen := filepath.Join(bin, "seen")
	fakeTool(t, bin, "udevil", `echo "$#" > "`+seen+`"; for a in "$@"; do echo "$a" >> "`+seen+`"; done`)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	cmd := exec.Command("bash", "-c", `source "$0"; sudo_cmd=""; UDEVILUmount "$1"`, filepath.Join(shell, "local-storage-helper.sh"), "/media/My Passport")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	got, err := os.ReadFile(seen)
	if err != nil {
		t.Fatal(err)
	}
	if want := "3\numount\n-f\n/media/My Passport\n"; string(got) != want {
		t.Errorf("udevil was given %q, want %q", got, want)
	}
}
