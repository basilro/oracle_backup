package main

import "testing"

func TestRedactCreds(t *testing.T) {
	in := "repo=rest:http://user:s3cr3t@rclone:8080/ ok"
	got := Redact(in)
	if got != "repo=rest:http://***:***@rclone:8080/ ok" {
		t.Fatalf("got %q", got)
	}
}

func TestRedactNoCreds(t *testing.T) {
	if Redact("plain line") != "plain line" {
		t.Fatal("should be unchanged")
	}
}
