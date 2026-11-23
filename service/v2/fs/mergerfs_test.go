package fs

import (
	"strings"
	"testing"

	"github.com/ReCasaOS/CasaOS-LocalStorage/codegen"
)

func TestMergerFSPreMountKeepsAllowOther(t *testing.T) {
	fstype, source := MergerFS, "/var/lib/casaos/files:/mnt/data"
	m := (&mergerFS{}).PreMount(codegen.Mount{Fstype: &fstype, Source: &source})

	for _, want := range []string{"allow_other", "use_ino", "fsname=" + source} {
		if !strings.Contains(*m.Options, want) {
			t.Errorf("options %q lack %q", *m.Options, want)
		}
	}
}
