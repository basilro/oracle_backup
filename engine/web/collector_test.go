package main

import "testing"

func TestParseSummary(t *testing.T) {
	stdout := `{"message_type":"status","percent_done":0.5}
{"message_type":"summary","files_new":5,"data_added":4612345,"total_bytes_processed":6890000000,"snapshot_id":"cb8e5520c33cb1824e9610344773112a"}`
	sum, err := ParseBackupSummary([]byte(stdout))
	if err != nil {
		t.Fatal(err)
	}
	if sum.DataAdded != 4612345 {
		t.Fatalf("data_added=%d", sum.DataAdded)
	}
	if sum.SnapshotID != "cb8e5520c33cb1824e9610344773112a" {
		t.Fatalf("snap=%s", sum.SnapshotID)
	}
}

func TestParseSummaryMissing(t *testing.T) {
	if _, err := ParseBackupSummary([]byte(`{"message_type":"status"}`)); err == nil {
		t.Fatal("expected error when no summary")
	}
}
