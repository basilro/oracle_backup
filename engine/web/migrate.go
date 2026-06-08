package main

import (
	"os"
	"strings"
)

const (
	remotePathFile      = "/config/remote-path"
	migrationStatusFile = "/state/migration-status.json"
)

// activeRemote returns the configured rclone remote name (REMOTE_NAME env).
func activeRemote() string { return os.Getenv("REMOTE_NAME") }

// defaultRepoPath is the built-in subpath used when no override file exists.
func defaultRepoPath() string { return "backups/" + os.Getenv("HOST_TAG") }

// currentRepoPath returns the active repo subpath using the standard override file.
func currentRepoPath() string { return currentRepoPathFrom(remotePathFile) }

// currentRepoPathFrom reads the override at the given path (testable seam):
// the file's contents if present and non-empty, otherwise the default.
func currentRepoPathFrom(file string) string {
	if b, err := os.ReadFile(file); err == nil {
		if p := strings.TrimSpace(string(b)); p != "" {
			return p
		}
	}
	return defaultRepoPath()
}

// repoURL builds the full restic repo URL (rclone backend) for a subpath.
func repoURL(path string) string { return "rclone:" + activeRemote() + ":" + path }

// validRemotePath validates a user-chosen rclone destination subpath. Rejects
// empty, leading "/" or "-", ".." traversal, control chars, and shell/rclone
// metacharacters (incl. ":" which would confuse the remote:path syntax).
func validRemotePath(p string) bool {
	p = strings.TrimSpace(p)
	if p == "" || strings.HasPrefix(p, "/") || strings.HasPrefix(p, "-") {
		return false
	}
	if strings.Contains(p, "..") {
		return false
	}
	for _, r := range p {
		if r < 0x20 || r == 0x7f {
			return false
		}
		switch r {
		case '`', '$', ';', '|', '&', '<', '>', '"', '\'', '\\', '*', '?', ':':
			return false
		}
	}
	return true
}

// writeRemotePath atomically persists the active subpath (read by entrypoint).
func writeRemotePath(path string) error {
	tmp := remotePathFile + ".tmp"
	if err := os.WriteFile(tmp, []byte(path+"\n"), 0644); err != nil {
		return err
	}
	return os.Rename(tmp, remotePathFile)
}
