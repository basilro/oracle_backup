package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"time"
)

type BackupSummary struct {
	MessageType string `json:"message_type"`
	DataAdded   int64  `json:"data_added"`
	TotalBytes  int64  `json:"total_bytes_processed"`
	SnapshotID  string `json:"snapshot_id"`
}

// ParseBackupSummary scans restic --json stdout lines, returns the final summary object.
func ParseBackupSummary(stdout []byte) (BackupSummary, error) {
	var found bool
	var sum BackupSummary
	sc := bufio.NewScanner(bytes.NewReader(stdout))
	sc.Buffer(make([]byte, 0, 1024*1024), 8*1024*1024)
	for sc.Scan() {
		var o BackupSummary
		if json.Unmarshal(sc.Bytes(), &o) == nil && o.MessageType == "summary" {
			sum = o
			found = true
		}
	}
	if !found {
		return sum, errors.New("no summary object in restic json output")
	}
	return sum, nil
}

type Snapshot struct {
	ID       string   `json:"id"`
	ShortID  string   `json:"short_id"`
	Time     string   `json:"time"`
	Hostname string   `json:"hostname"`
	Tags     []string `json:"tags"`
	Paths    []string `json:"paths"`
}

// resticJSON runs a restic subcommand with a timeout and returns stdout.
func resticJSON(ctx context.Context, args ...string) ([]byte, error) {
	cctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(cctx, "restic", args...)
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func ListSnapshots(ctx context.Context) ([]Snapshot, error) {
	b, err := resticJSON(ctx, "snapshots", "--json")
	if err != nil {
		return nil, err
	}
	var s []Snapshot
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, err
	}
	return s, nil
}
