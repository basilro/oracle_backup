package main

import "testing"

func TestValidSourcePaths(t *testing.T) {
	ok := []string{"/home", "/home /srv/data", "/opt/docker"}
	bad := []string{"", "home", "/home/../etc", "/etc", "/", "/var"}
	for _, s := range ok {
		if err := validSourcePaths(s); err != nil {
			t.Errorf("expected valid %q: %v", s, err)
		}
	}
	for _, s := range bad {
		if err := validSourcePaths(s); err == nil {
			t.Errorf("expected invalid: %q", s)
		}
	}
}
