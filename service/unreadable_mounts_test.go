package service

import (
	"context"
	"testing"

	"gotest.tools/v3/assert"
)

func TestParseDfComplaintsKeepsTheDeadMountsOnly(t *testing.T) {
	stderr := "df: /media/ser/fotos: Stale file handle\n" +
		"df: '/mnt/my share': Transport endpoint is not connected\n" +
		"df: '/mnt/it'\\''s': Input/output error\n" +
		"df: /run/user/1000/doc: Operation not permitted\n" +
		"df: /run/user/1000/gvfs: Permission denied\n" +
		"something else\n"

	assert.DeepEqual(t, parseDfComplaints(stderr), []UnreadableMount{
		{MountPoint: "/media/ser/fotos", Error: "Stale file handle"},
		{MountPoint: "/mnt/my share", Error: "Transport endpoint is not connected"},
		{MountPoint: "/mnt/it's", Error: "Input/output error"},
	})
	assert.DeepEqual(t, parseDfComplaints(""), []UnreadableMount{})
}

func TestUnreadableMountsSaysWhenDfHangs(t *testing.T) {
	saved := dfComplaints
	t.Cleanup(func() { dfComplaints = saved })
	dfComplaints = func(ctx context.Context) (string, error) { return "", context.DeadlineExceeded }

	mounts, err := UnreadableMounts()

	assert.NilError(t, err)
	assert.DeepEqual(t, mounts, []UnreadableMount{{Error: "df did not answer within 15 s"}})
}
