package main

import "os"

// atomicWrite writes data to path atomically (temp file + rename) with 0644.
// Used for config/state files that other processes (the bash engine) may read
// concurrently, so a reader never sees a half-written file.
func atomicWrite(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
