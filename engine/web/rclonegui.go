package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// On-demand rclone Web GUI: the engine spawns a sibling container (same image)
// running `rclone rcd --rc-web-gui` with ./rclone bind-mounted READ-WRITE.
//
// SECURITY: the rclone RC API is powerful (config/dump returns all cloud tokens
// in plaintext, operations/* read/write/delete cloud data, core/command runs
// arbitrary rclone). It is therefore bound to LOOPBACK only (127.0.0.1) and must
// be reached via an SSH tunnel — never exposed on the LAN. A hard TTL auto-stops
// it, and the engine reconciles any orphan on boot.
const (
	rgName = "backupstack_rclonegui"
	rgPort = "5572"
	rgTTL  = 30 * time.Minute
)

var (
	rgMu    sync.Mutex
	rgTimer *time.Timer
	// rgAudit, if set by main(), records GUI lifecycle events.
	rgAudit func(action, result string)
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
	out, err := exec.Command("docker", "inspect", ref).CombinedOutput()
	if err != nil {
		if strings.Contains(string(out), "No such") {
			return nil, errNotFound
		}
		return nil, fmt.Errorf("docker inspect: %v: %s", err, strings.TrimSpace(string(out)))
	}
	var a []dInspect
	if json.Unmarshal(out, &a) != nil || len(a) == 0 {
		return nil, errors.New("inspect parse")
	}
	return &a[0], nil
}

var errNotFound = errors.New("not found")

// rgBind is the HOST publish address for the GUI port. Default 127.0.0.1
// (reach via SSH tunnel). Set RCLONE_GUI_BIND=0.0.0.0 when host access is
// already controlled upstream (NAT gateway / domain forwarding).
func rgBind() string {
	if b := os.Getenv("RCLONE_GUI_BIND"); b != "" {
		return b
	}
	return "127.0.0.1"
}

// selfRef identifies the engine's own container (env override, else hostname).
func selfRef() (string, error) {
	if v := os.Getenv("SELF_CONTAINER"); v != "" {
		return v, nil
	}
	h, err := os.Hostname()
	if err != nil || h == "" {
		return "", errors.New("self container ref 확인 불가 (SELF_CONTAINER 미설정 + hostname 없음)")
	}
	return h, nil
}

// rcloneGUIRunning reports whether the GUI container exists and is running.
// Docker errors other than not-found are logged (not silently treated as down).
func rcloneGUIRunning() bool {
	d, err := dockerInspect(rgName)
	if err != nil {
		if !errors.Is(err, errNotFound) {
			log.Printf("rclone-gui status: %v", err)
		}
		return false
	}
	return d.State.Running
}

// startRcloneGUI (re)launches the loopback-bound GUI container; returns the password.
func startRcloneGUI() (string, error) {
	ref, err := selfRef()
	if err != nil {
		return "", err
	}
	self, err := dockerInspect(ref)
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
	_ = forceRemove(rgName) // clear any stale one

	// HOST publish binds per rgBind() (default 127.0.0.1). The in-container listener
	// MUST be 0.0.0.0 or docker's port proxy (which targets the container's bridge
	// IP, not its loopback) can't deliver. Auth + TTL guard the rc API regardless.
	args := []string{
		"run", "-d", "--name", rgName, "--restart", "no",
		"-p", rgBind() + ":" + rgPort + ":5572",
		"--mount", "type=bind,source=" + hostDir + ",destination=/etc/rclone",
		"--entrypoint", "rclone", img,
		"rcd", "--rc-web-gui", "--rc-web-gui-no-open-browser",
		"--rc-addr", "0.0.0.0:5572",
		"--rc-user", "admin", "--rc-pass", pass,
		"--config", "/etc/rclone/rclone.conf",
	}
	if out, err := exec.Command("docker", args...).CombinedOutput(); err != nil {
		_ = forceRemove(rgName) // no orphan with an unknown password
		return "", fmt.Errorf("기동 실패: %v: %s", err, strings.TrimSpace(string(out)))
	}

	rgMu.Lock()
	if rgTimer != nil {
		rgTimer.Stop()
	}
	rgTimer = time.AfterFunc(rgTTL, func() {
		log.Printf("rclone-gui: TTL reached, auto-stopping")
		_ = stopRcloneGUI()
		if rgAudit != nil {
			rgAudit("rclone-gui-stop", "auto")
		}
	})
	rgMu.Unlock()
	log.Printf("rclone-gui started on host %s:%s (auto-stop in %s)", rgBind(), rgPort, rgTTL)
	return pass, nil
}

func stopRcloneGUI() error {
	rgMu.Lock()
	if rgTimer != nil {
		rgTimer.Stop()
		rgTimer = nil
	}
	rgMu.Unlock()
	return forceRemove(rgName)
}

// forceRemove deletes the named container, treating "no such container" as success.
func forceRemove(name string) error {
	out, err := exec.Command("docker", "rm", "-f", name).CombinedOutput()
	if err != nil {
		if strings.Contains(string(out), "No such container") {
			return nil
		}
		return fmt.Errorf("docker rm: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}
