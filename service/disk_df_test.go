package service

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"gotest.tools/v3/assert"
)

// A df that exits 1 over one unreadable mount, as df does, still gives the system line;
// this was log.Fatal, which stopped the service.
func TestGetSystemDfReadsTheOutputOfADfThatExitsOne(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("needs a shell script named df")
	}
	dir := t.TempDir()
	script := "#!/bin/sh\n" +
		"echo 'Filesystem Type 1024-blocks Used Available Capacity Mounted on'\n" +
		"echo '/dev/sda2 ext4 1000 400 600 40% /'\n" +
		"echo 'df: /mnt/dead: Transport endpoint is not connected' >&2\n" +
		"exit 1\n"
	assert.NilError(t, os.WriteFile(filepath.Join(dir, "df"), []byte(script), 0o755))
	t.Setenv("PATH", dir)

	df, err := (&diskService{}).GetSystemDf()

	assert.NilError(t, err)
	assert.Equal(t, df.FileSystem, "/dev/sda2")
	assert.Equal(t, df.Used, "409600")
}

func TestGetSystemDfIsAnErrorWhenDfGivesNothing(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("needs a shell script named df")
	}
	dir := t.TempDir()
	assert.NilError(t, os.WriteFile(filepath.Join(dir, "df"), []byte("#!/bin/sh\nexit 1\n"), 0o755))
	t.Setenv("PATH", dir)

	_, err := (&diskService{}).GetSystemDf()

	assert.Assert(t, err != nil)
}
