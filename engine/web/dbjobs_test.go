package main

import "testing"

func TestValidDBJob(t *testing.T) {
	ok := DBJob{Name: "pg-main", Type: "postgres", Container: "postgres", Data: "/home/docker/pg", Enabled: true}
	if err := validDBJob(ok); err != nil {
		t.Errorf("expected valid: %v", err)
	}
	if validDBJob(DBJob{Name: "x", Type: "oracle", Container: "c"}) == nil {
		t.Error("unknown type must fail")
	}
	if validDBJob(DBJob{Name: "bad name", Type: "redis", Container: "c"}) == nil {
		t.Error("bad name must fail")
	}
	if validDBJob(DBJob{Name: "x", Type: "redis", Container: "c", Data: "../etc"}) == nil {
		t.Error("relative data must fail")
	}
	if err := validDBJob(DBJob{Name: "x", Type: "redis", Container: "c", Data: ""}); err != nil {
		t.Errorf("empty data should be allowed: %v", err)
	}
	for _, ty := range []string{"mysql", "mariadb"} {
		if err := validDBJob(DBJob{Name: "x", Type: ty, Container: "c"}); err != nil {
			t.Errorf("%s should be valid: %v", ty, err)
		}
	}
}

func TestValidDBJobsUnique(t *testing.T) {
	dup := []DBJob{
		{Name: "a", Type: "redis", Container: "c1"},
		{Name: "a", Type: "redis", Container: "c2"},
	}
	if validDBJobs(dup) == nil {
		t.Error("duplicate names must fail")
	}
	if err := validDBJobs([]DBJob{}); err != nil {
		t.Errorf("empty list must be allowed (DB-less server): %v", err)
	}
}
