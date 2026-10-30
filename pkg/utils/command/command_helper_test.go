package command

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func stubOnPATH(t *testing.T, name, script string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+script+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

const fixture = `{"blockdevices":[{"name":"vda","path":"/dev/vda","children":[{"name":"vda1","path":"/dev/vda1"}]},{"name":"vdb","path":"/dev/vdb"}]}`

func TestExecLSBLKFromFile(t *testing.T) {
	stubOnPATH(t, "lsblk", "echo CALLED >&2; exit 1")
	p := filepath.Join(t.TempDir(), "disks.json")
	if err := os.WriteFile(p, []byte(fixture), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CASAOS_LSBLK_FILE", p)
	got := ExecLSBLK()
	if string(got) != fixture {
		t.Fatalf("got %q", got)
	}
}

func TestExecLSBLKUnsetUsesHost(t *testing.T) {
	t.Setenv("CASAOS_LSBLK_FILE", "")
	marker := filepath.Join(t.TempDir(), "called")
	stubOnPATH(t, "lsblk", "echo -n '{\"blockdevices\":[]}' ; echo yes >"+marker)
	got := ExecLSBLK()
	if !strings.Contains(string(got), "blockdevices") {
		t.Fatalf("got %q", got)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatal("host lsblk not invoked")
	}
}

func TestExecLSBLKByPathFilters(t *testing.T) {
	stubOnPATH(t, "lsblk", "exit 1")
	p := filepath.Join(t.TempDir(), "disks.json")
	if err := os.WriteFile(p, []byte(fixture), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CASAOS_LSBLK_FILE", p)
	got := ExecLSBLKByPath("/dev/vdb")
	if !strings.Contains(string(got), `"path":"/dev/vdb"`) {
		t.Fatalf("got %q", got)
	}
	if strings.Contains(string(got), "/dev/vda") {
		t.Fatalf("unfiltered %q", got)
	}
	none := ExecLSBLKByPath("/dev/vdz")
	if !strings.Contains(string(none), `"blockdevices":[]`) && !strings.Contains(string(none), `"blockdevices": []`) {
		t.Fatalf("empty match %q", none)
	}
}

func TestExecLSBLKUnreadable(t *testing.T) {
	t.Setenv("CASAOS_LSBLK_FILE", filepath.Join(t.TempDir(), "missing.json"))
	if got := ExecLSBLK(); got != nil {
		t.Fatalf("got %q", got)
	}
}

func TestFakeStoragePartedNoop(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "called")
	stubOnPATH(t, "parted", "echo yes >"+marker+"; exit 1")
	t.Setenv("CASAOS_FAKE_STORAGE", "1")
	out, err := ExecuteCommand("parted", "-s", "/dev/vdb", "print")
	if err != nil || out != nil {
		t.Fatalf("out=%q err=%v", out, err)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("parted invoked")
	}
}

func TestPartedUnsetExecs(t *testing.T) {
	t.Setenv("CASAOS_FAKE_STORAGE", "")
	marker := filepath.Join(t.TempDir(), "called")
	stubOnPATH(t, "parted", "echo -n ok; echo yes >"+marker)
	out, err := ExecuteCommand("parted", "-s", "/dev/vdb", "print")
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != "ok" {
		t.Fatalf("out %q", out)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatal("parted not invoked")
	}
}
