package model

import (
	"path/filepath"
	"strings"

	"github.com/ReCasaOS/CasaOS-Common/utils/file"
)

const defaultMountPath = "/media"

func (m *LSBLKModel) GetMountPoint(name string) string {
	if name == "" {
		name = "Storage_" + m.Name
	}

	if m.Label != "" {
		name += "_" + m.Label
	}

	if m.Model != "" {
		name += "_" + m.Model
	}
	// One folder of /media: the label and the model are what the disk says they are,
	// and a "/" or a ".." in them would put the mount point anywhere on the box.
	mountPoint := filepath.Join(defaultMountPath, strings.ReplaceAll(name, "/", "_"))
	if filepath.Dir(mountPoint) != defaultMountPath {
		mountPoint = filepath.Join(defaultMountPath, "Storage_"+strings.ReplaceAll(m.Name, "/", "_"))
	}
	if file.CheckNotExist(mountPoint) {
		return mountPoint
	}
	return mountPoint + "_" + m.Name
}
