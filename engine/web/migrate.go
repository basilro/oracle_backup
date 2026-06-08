package main

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"
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

// remoteConnected reports whether the active remote answers a shallow listing.
func remoteConnected(ctx context.Context) bool {
	cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	return exec.CommandContext(cctx, "rclone", "lsd", activeRemote()+":", "--max-depth", "1").Run() == nil
}

// handleRemotePath (GET): current remote + active subpath + connectivity.
func (s *Server) handleRemotePath(w http.ResponseWriter, r *http.Request) {
	s.writeJSON(w, 200, map[string]any{
		"remote":    activeRemote(),
		"path":      currentRepoPath(),
		"connected": remoteConnected(r.Context()),
	})
}

// handleRemoteLs (GET ?path=): list folders (dirs only) under the active remote.
func (s *Server) handleRemoteLs(w http.ResponseWriter, r *http.Request) {
	if activeRemote() == "" {
		s.writeJSON(w, 400, map[string]string{"error": "원격이 설정되지 않았습니다"})
		return
	}
	path := strings.Trim(strings.TrimSpace(r.URL.Query().Get("path")), "/")
	if path != "" && !validRemotePath(path) {
		http.Error(w, "bad path", 400)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "rclone", "lsjson", activeRemote()+":"+path, "--dirs-only").Output()
	if err != nil {
		s.writeJSON(w, 502, map[string]string{"error": "원격 조회 실패 (연결을 확인하세요)"})
		return
	}
	var raw []struct {
		Name  string `json:"Name"`
		IsDir bool   `json:"IsDir"`
	}
	json.Unmarshal(out, &raw)
	type entry struct {
		Name string `json:"name"`
		Path string `json:"path"`
	}
	entries := []entry{}
	for _, e := range raw {
		if !e.IsDir {
			continue
		}
		child := e.Name
		if path != "" {
			child = path + "/" + e.Name
		}
		entries = append(entries, entry{Name: e.Name, Path: child})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	s.writeJSON(w, 200, map[string]any{"path": path, "entries": entries})
}
