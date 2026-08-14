// Package sharedfs holds process-level filesystem settings that must be
// consistent across the runner and every task executor when a run uses a
// shared workspace.
package sharedfs

import (
	"log/slog"
	"os"
	"syscall"
)

// SharedWorkspaceEnv is set on the runner and on task executor containers
// whenever the run's workspace is shared between pods (pvc or objectfs).
const SharedWorkspaceEnv = "CONQUEST_SHARED_WORKSPACE"

// ApplyUmask clears the process umask when the run uses a shared workspace.
//
// Why this is needed:
//
// A umask is a mask of permission bits the kernel *removes* at file creation.
// The default in virtually every container image is 022, so open(0666) yields
// a 0644 file and mkdir(0777) yields a 0755 directory, both owned by whoever
// created them.
//
// On a shared workspace that is fatal across UIDs. The runner container is
// usually root, while task executors run as whatever UID the workflow's image
// declares — frequently non-root. With umask 022 the runner's git checkout and
// intermediate directories land as root:root 0755/0644, and a task running as
// UID 1000 then gets EACCES writing its outputs into them.
//
// It bites specifically on objectfs because GeeseFS is mounted with
// --enable-perms (required so chmod/utime round-trip, which miniwdl depends on)
// and FUSE enforces those stored modes via default_permissions. The mount-wide
// --dir-mode/--file-mode values are only fallbacks for objects with no stored
// mode, so they do not rescue files the runner created.
//
// Clearing the umask makes new files 0666 and new directories 0777, so any UID
// in the pod set can write. Explicit chmod() still works and is still persisted
// — we are only declining to strip group/other bits at creation time.
//
// This is a no-op for emptydir runs, where each task gets a private volume and
// tightening the umask costs nothing.
func ApplyUmask(log *slog.Logger) {
	if os.Getenv(SharedWorkspaceEnv) != "true" {
		return
	}
	prev := syscall.Umask(0)
	if log != nil {
		log.Info("cleared umask for shared workspace",
			"previous_umask", "0"+itoa8(prev),
			"new_umask", "0000",
		)
	}
}

// itoa8 renders a umask as octal without pulling in fmt.
func itoa8(v int) string {
	if v == 0 {
		return "000"
	}
	var buf [4]byte
	i := len(buf)
	for v > 0 && i > 0 {
		i--
		buf[i] = byte('0' + v&7)
		v >>= 3
	}
	return string(buf[i:])
}
