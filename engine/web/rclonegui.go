package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
)

// On-demand rclone Web GUI: the engine spawns a sibling container (same image)
// running `rclone rcd --rc-web-gui` with ./rclone bind-mounted READ-WRITE, so
// the operator configures remotes in rclone's own UI and the result lands in
// rclone/rclone.conf. The running engine keeps rclone.conf read-only.
const (
	rgName = "backupstack_rclonegui"
	rgPort = "5572"
)

type dInspect struct {
	Mounts []struct {
		Source      string `json:"Source"`
		Destination string `json:"Destination"`
	} `json:"Mounts"`
	Config struct {
		Image string `json:"Image"`
	} `json:"Config"`
	State struct {
		Running bool `json:"Running"`
	} `json:"State"`
}

func dockerInspect(ref string) (*dInspect, error) {
	out, err := exec.Command("docker", "inspect", ref).Output()
	if err != nil {
		return nil, err
	}
	var a []dInspect
	if json.Unmarshal(out, &a) != nil || len(a) == 0 {
		return nil, errors.New("inspect parse")
	}
	return &a[0], nil
}

func selfRef() string { h, _ := os.Hostname(); return h }

// rcloneGUIRunning reports whether the GUI container exists and is running.
func rcloneGUIRunning() bool {
	d, err := dockerInspect(rgName)
	return err == nil && d.State.Running
}

// startRcloneGUI (re)launches the GUI container and returns the generated password.
func startRcloneGUI() (string, error) {
	self, err := dockerInspect(selfRef())
	if err != nil {
		return "", fmt.Errorf("engine 컨테이너 inspect 실패: %v", err)
	}
	hostDir := ""
	for _, m := range self.Mounts {
		if m.Destination == "/etc/rclone" {
			hostDir = m.Source
		}
	}
	if hostDir == "" {
		return "", errors.New("/etc/rclone 마운트(호스트 경로)를 찾을 수 없음")
	}
	img := self.Config.Image
	if img == "" {
		return "", errors.New("engine 이미지 이름을 확인할 수 없음")
	}
	pass := randPassword(20)
	_ = exec.Command("docker", "rm", "-f", rgName).Run() // remove any stale one

	args := []string{
		"run", "-d", "--name", rgName, "--restart", "no",
		"-p", rgPort + ":5572",
		"-v", hostDir + ":/etc/rclone",
		"--entrypoint", "rclone", img,
		"rcd", "--rc-web-gui", "--rc-web-gui-no-open-browser",
		"--rc-addr", "0.0.0.0:5572",
		"--rc-user", "admin", "--rc-pass", pass,
		"--config", "/etc/rclone/rclone.conf",
	}
	if out, err := exec.Command("docker", args...).CombinedOutput(); err != nil {
		return "", fmt.Errorf("기동 실패: %v: %s", err, string(out))
	}
	return pass, nil
}

func stopRcloneGUI() error {
	out, err := exec.Command("docker", "rm", "-f", rgName).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%v: %s", err, string(out))
	}
	return nil
}
