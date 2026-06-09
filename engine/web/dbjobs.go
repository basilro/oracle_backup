package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"strings"
)

const dbJobsFile = "/config/db-jobs.json"

type DBJob struct {
	Name      string `json:"name"`
	Type      string `json:"type"`
	Container string `json:"container"`
	Data      string `json:"data"`
	Enabled   bool   `json:"enabled"`
}

var dbJobNameRe = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,64}$`)
var dbJobTypes = map[string]bool{"postgres": true, "mongodb": true, "redis": true}

// defaultDBJobs mirrors the legacy hardcoded 3-type behavior (shown when no file).
func defaultDBJobs() []DBJob {
	return []DBJob{
		{Name: "postgres", Type: "postgres", Container: "postgres", Data: "/home/docker/postgres/data", Enabled: true},
		{Name: "mongodb", Type: "mongodb", Container: "mongodb", Data: "/home/docker/mongodb", Enabled: true},
		{Name: "redis", Type: "redis", Container: "redis", Data: "/home/docker/redis", Enabled: true},
	}
}

// validDBJob validates one job. data: empty or absolute, no "..", no control chars.
func validDBJob(j DBJob) error {
	if !dbJobNameRe.MatchString(j.Name) {
		return fmt.Errorf("이름 형식 오류: %q", j.Name)
	}
	if !dbJobTypes[j.Type] {
		return fmt.Errorf("지원하지 않는 유형: %q", j.Type)
	}
	if !dbJobNameRe.MatchString(j.Container) {
		return fmt.Errorf("컨테이너 이름 형식 오류: %q", j.Container)
	}
	if j.Data != "" {
		if !strings.HasPrefix(j.Data, "/") || strings.Contains(j.Data, "..") {
			return fmt.Errorf("데이터 경로 오류: %q", j.Data)
		}
		for _, r := range j.Data {
			if r < 0x20 || r == 0x7f {
				return fmt.Errorf("데이터 경로에 제어문자")
			}
		}
	}
	return nil
}

// validDBJobs validates the whole list (each job + unique names).
func validDBJobs(jobs []DBJob) error {
	seen := map[string]bool{}
	for _, j := range jobs {
		if err := validDBJob(j); err != nil {
			return err
		}
		if seen[j.Name] {
			return fmt.Errorf("이름 중복: %q", j.Name)
		}
		seen[j.Name] = true
	}
	return nil
}

func readDBJobs() ([]DBJob, bool) {
	b, err := os.ReadFile(dbJobsFile)
	if err != nil {
		return defaultDBJobs(), true // defaults flag
	}
	var jobs []DBJob
	if json.Unmarshal(b, &jobs) != nil {
		return defaultDBJobs(), true
	}
	return jobs, false
}

func writeDBJobs(jobs []DBJob) error {
	b, err := json.MarshalIndent(jobs, "", "  ")
	if err != nil {
		return err
	}
	tmp := dbJobsFile + ".tmp"
	if err := os.WriteFile(tmp, b, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, dbJobsFile)
}

// handleDBJobs: GET → {jobs, defaults}; POST {jobs:[...]} → validate + save.
func (s *Server) handleDBJobs(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		jobs, defaults := readDBJobs()
		s.writeJSON(w, 200, map[string]any{"jobs": jobs, "defaults": defaults})
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
		Jobs []DBJob `json:"jobs"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad", 400)
		return
	}
	if err := validDBJobs(body.Jobs); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if err := writeDBJobs(body.Jobs); err != nil {
		s.writeJSON(w, 500, map[string]string{"error": "저장 실패"})
		return
	}
	s.store.Audit(user, "db-jobs", fmt.Sprintf("save:%d", len(body.Jobs)))
	s.writeJSON(w, 200, map[string]any{"jobs": body.Jobs, "defaults": false})
}
