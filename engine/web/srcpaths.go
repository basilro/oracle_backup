package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
)

const sourcePathsFile = "/config/source-paths"

// readSourcePaths returns configured source paths string (default "/home").
func readSourcePaths() string {
	b, err := os.ReadFile(sourcePathsFile)
	if err != nil {
		return "/home"
	}
	if s := strings.TrimSpace(string(b)); s != "" {
		return s
	}
	return "/home"
}

// validSourcePaths checks each whitespace-separated path is absolute, has no
// "..", and is not a dangerous root.
func validSourcePaths(s string) error {
	fields := strings.Fields(s)
	if len(fields) == 0 {
		return fmt.Errorf("소스 경로가 비어 있습니다")
	}
	dangerous := map[string]bool{"/": true, "/etc": true, "/root": true, "/var": true, "/boot": true, "/usr": true, "/bin": true, "/sbin": true, "/lib": true, "/proc": true, "/sys": true, "/dev": true}
	for _, p := range fields {
		if !strings.HasPrefix(p, "/") {
			return fmt.Errorf("절대경로여야 합니다: %q", p)
		}
		if strings.Contains(p, "..") {
			return fmt.Errorf(".. 포함 불가: %q", p)
		}
		clean := strings.TrimRight(p, "/")
		if clean == "" {
			clean = "/"
		}
		if dangerous[clean] {
			return fmt.Errorf("위험 경로 불가: %q", p)
		}
	}
	return nil
}

func writeSourcePaths(s string) error {
	tmp := sourcePathsFile + ".tmp"
	if err := os.WriteFile(tmp, []byte(strings.TrimSpace(s)+"\n"), 0644); err != nil {
		return err
	}
	return os.Rename(tmp, sourcePathsFile)
}

// handleSourcePaths: GET → {paths}; POST {paths} → validate + save.
func (s *Server) handleSourcePaths(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		s.writeJSON(w, 200, map[string]string{"paths": readSourcePaths()})
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method", 405)
		return
	}
	if !s.checkCSRF(r) {
		http.Error(w, "csrf", 403)
		return
	}
	user, _ := s.currentUser(r)
	var body struct {
		Paths string `json:"paths"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad", 400)
		return
	}
	if err := validSourcePaths(body.Paths); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if err := writeSourcePaths(body.Paths); err != nil {
		s.writeJSON(w, 500, map[string]string{"error": "저장 실패"})
		return
	}
	s.store.Audit(user, "source-paths", "set")
	s.writeJSON(w, 200, map[string]string{"paths": strings.TrimSpace(body.Paths)})
}
