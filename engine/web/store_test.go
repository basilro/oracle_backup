package main

import (
	"path/filepath"
	"testing"
)

func TestStoreRunLifecycle(t *testing.T) {
	db := filepath.Join(t.TempDir(), "h.db")
	s, err := OpenStore(db)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	id, err := s.StartRun("manual")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.FinishRun(id, "ok", 0, 12345, "abc123", `{"postgres":{"state":"DUMPED_OK"}}`, ""); err != nil {
		t.Fatal(err)
	}
	runs, err := s.ListRuns(10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || runs[0].Status != "ok" || runs[0].DataAdded != 12345 {
		t.Fatalf("bad: %+v", runs)
	}
}

func TestReconcileStuck(t *testing.T) {
	db := filepath.Join(t.TempDir(), "h.db")
	s, _ := OpenStore(db)
	defer s.Close()
	id, _ := s.StartRun("scheduled")
	if err := s.ReconcileStuck(); err != nil {
		t.Fatal(err)
	}
	r, _ := s.GetRun(id)
	if r.Status != "interrupted" {
		t.Fatalf("want interrupted got %s", r.Status)
	}
}
