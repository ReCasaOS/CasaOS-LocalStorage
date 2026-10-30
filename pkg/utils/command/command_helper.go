package command

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	exec2 "github.com/ReCasaOS/CasaOS-Common/utils/exec"
)

// exec smart
func ExecSmartCTLByPath(path string) []byte {
	timeout := 6
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeout)*time.Second)
	defer cancel()
	// smartctl -i -n standby /dev/sdc  TODO:https://www.ippa.top/956.html
	cmd := exec2.CommandContext(ctx, "smartctl", "-a", "-n", "standby", path, "-j")

	output, err := cmd.Output()
	if err != nil {
		fmt.Println("smartctl", err.Error())
		fmt.Println("smartctl", string(path))
		fmt.Println("smartctl", len(output))
	}
	return output
}

func ExecEnabledSMART(path string) ([]byte, error) {
	return exec2.Command("smartctl", "-s", "on", path).CombinedOutput()
}

func lsblkFile() string { return os.Getenv("CASAOS_LSBLK_FILE") }

func fakeStorage() bool { return os.Getenv("CASAOS_FAKE_STORAGE") == "1" }

func readLSBLKFile() []byte {
	p := lsblkFile()
	if p == "" {
		return nil
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		return nil
	}
	return raw
}

func filterLSBLK(raw []byte, path string) []byte {
	var doc struct {
		Blockdevices []map[string]any `json:"blockdevices"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return []byte(`{"blockdevices":[]}`)
	}
	match := make([]map[string]any, 0)
	for _, d := range doc.Blockdevices {
		name, _ := d["name"].(string)
		p, _ := d["path"].(string)
		if p == path || p == "/dev/"+strings.TrimPrefix(path, "/dev/") || "/dev/"+name == path || name == path {
			match = append(match, d)
			continue
		}
		children, _ := d["children"].([]any)
		for _, c := range children {
			cm, _ := c.(map[string]any)
			cn, _ := cm["name"].(string)
			cp, _ := cm["path"].(string)
			if cp == path || "/dev/"+cn == path || cn == path {
				match = append(match, cm)
			}
		}
	}
	out, err := json.Marshal(map[string]any{"blockdevices": match})
	if err != nil {
		return []byte(`{"blockdevices":[]}`)
	}
	return out
}

// 执行 lsblk 命令
func ExecLSBLKByPath(path string) []byte {
	if lsblkFile() != "" {
		raw := readLSBLKFile()
		if raw == nil {
			return nil
		}
		return filterLSBLK(raw, path)
	}
	output, err := exec2.Command("lsblk", path, "-O", "-J", "-b").Output()
	if err != nil {
		fmt.Println("lsblk", err)
		return nil
	}
	return output
}

// 执行 lsblk 命令
func ExecLSBLK() []byte {
	if lsblkFile() != "" {
		return readLSBLKFile()
	}
	output, err := exec2.Command("lsblk", "-O", "-J", "-b").Output()
	if err != nil {
		fmt.Println("lsblk", err)
		return nil
	}
	return output
}

// ExecFilesystemLabel reads the label directly from the filesystem. This is
// useful immediately after a label change, before the udev database used by
// lsblk has caught up.
func ExecFilesystemLabel(path string) string {
	output, err := exec2.Command("blkid", "-o", "value", "-s", "LABEL", path).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(output))
}

func ExecuteCommand(name string, arg ...string) ([]byte, error) {
	if fakeStorage() {
		switch name {
		case "mount", "umount", "parted", "partprobe", "e2label", "sfdisk":
			return nil, nil
		}
	}
	cmd := exec2.Command(name, arg...)
	println(cmd.String())

	out, err := cmd.Output()
	println(string(out))
	if err != nil {
		if exitError, ok := err.(*exec.ExitError); ok {
			message := string(exitError.Stderr)
			println(message)
			return nil, errors.New(message)
		}
		return nil, err
	}

	return out, nil
}
