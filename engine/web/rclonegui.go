package main

import (
	"context"
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
// The GUI is NOT published to the host — it lives on the engine's docker network
// and is reached only through the engine's authenticated reverse proxy at
// /rclone-gui/ (see api.go). So no extra host port is exposed, the rclone RC API
// (config/dump etc.) never faces the LAN, and the GUI password (injected by the
// proxy) is never shown to the browser. A 30m TTL and boot reconcile clean up.
const (
	rgName = "backupstack_rclonegui"
	rgTTL  = 30 * time.Minute
)

var (
	rgMu     sync.Mutex
	rgTimer  *time.Timer
	rgActive bool                        // GUI running (proxy gate; fast, no docker call)
	rgAudit  func(action, result string) // set by main()
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
	NetworkSettings struct {
		Networks map[string]json.RawMessage `json:"Networks"`
	} `json:"NetworkSettings"`
}

var errNotFound = errors.New("not found")

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

// rcloneGUIActive reports whether the GUI is running (fast, in-memory; used by the proxy).
func rcloneGUIActive() bool {
	rgMu.Lock()
	defer rgMu.Unlock()
	return rgActive
}

// engineEnv inspects the engine's own container to learn the host path of the
// ./rclone mount, the image name, and its docker network — needed to spawn
// sibling containers (GUI, one-off config create) that can write rclone.conf.
func engineEnv() (hostDir, img, net string, err error) {
	ref, err := selfRef()
	if err != nil {
		return "", "", "", err
	}
	self, err := dockerInspect(ref)
	if err != nil {
		return "", "", "", fmt.Errorf("engine 컨테이너 inspect 실패: %v", err)
	}
	for _, m := range self.Mounts {
		if m.Destination == "/etc/rclone" {
			hostDir = m.Source
		}
	}
	img = self.Config.Image
	for n := range self.NetworkSettings.Networks {
		net = n
		break
	}
	if hostDir == "" || img == "" || net == "" {
		return hostDir, img, net, errors.New("engine 환경(마운트/이미지/네트워크) 확인 실패")
	}
	// defensive: values feed `docker run` argv — reject anything option-like.
	if !strings.HasPrefix(hostDir, "/") || strings.HasPrefix(img, "-") || strings.HasPrefix(net, "-") {
		return "", "", "", errors.New("비정상 engine 환경 값")
	}
	return hostDir, img, net, nil
}

// rcloneBackends maps a backend type to its allowed config parameter keys
// (non-OAuth backends configurable from a simple form).
var rcloneBackends = map[string][]string{
	"webdav": {"url", "vendor", "user", "pass"},
	"sftp":   {"host", "user", "pass", "port"},
	"ftp":    {"host", "user", "pass", "port"},
	"s3":     {"provider", "access_key_id", "secret_access_key", "endpoint", "region"},
}

// createRemote runs a one-off `rclone config create` (rw ./rclone mount) so the
// engine's read-only config stays untouched. rclone obscures passwords itself.
func createRemote(name, typ string, params [][2]string) error {
	hostDir, img, _, err := engineEnv()
	if err != nil {
		return err
	}
	args := []string{"run", "--rm",
		"--mount", "type=bind,source=" + hostDir + ",destination=/etc/rclone",
		"--entrypoint", "rclone", img,
		"config", "create", name, typ}
	for _, p := range params {
		if p[1] != "" {
			args = append(args, p[0]+"="+p[1])
		}
	}
	args = append(args, "--config", "/etc/rclone/rclone.conf")
	if out, err := exec.Command("docker", args...).CombinedOutput(); err != nil {
		return fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// rcloneCLIAllowed limits the web CLI to read/inspect + config-management
// subcommands; destructive data ops (delete/purge/sync/copy/move/rmdir…) are blocked.
var rcloneCLIAllowed = map[string]bool{
	"config": true, "listremotes": true, "lsd": true, "ls": true, "lsl": true,
	"lsf": true, "lsjson": true, "tree": true, "about": true, "size": true,
	"version": true, "cat": true, "hashsum": true, "md5sum": true, "sha1sum": true,
}

// splitArgs is a minimal quote-aware splitter (no shell; values become argv).
func splitArgs(s string) []string {
	var args []string
	var cur strings.Builder
	inq := false
	flush := func() {
		if cur.Len() > 0 {
			args = append(args, cur.String())
			cur.Reset()
		}
	}
	for _, r := range s {
		switch {
		case r == '"':
			inq = !inq
		case (r == ' ' || r == '\t') && !inq:
			flush()
		default:
			cur.WriteRune(r)
		}
	}
	flush()
	return args
}

// runRcloneCLI runs `rclone <args>` in a one-off container (rw ./rclone mount,
// default-bridge network for cloud access), 60s timeout. Returns combined output.
func runRcloneCLI(ctx context.Context, rcArgs []string) (string, error) {
	hostDir, img, _, err := engineEnv()
	if err != nil {
		return "", err
	}
	cctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	args := []string{"run", "--rm",
		"-e", "RCLONE_CONFIG=/etc/rclone/rclone.conf",
		"--mount", "type=bind,source=" + hostDir + ",destination=/etc/rclone",
		"--entrypoint", "rclone", img}
	args = append(args, rcArgs...)
	out, err := exec.CommandContext(cctx, "docker", args...).CombinedOutput()
	return string(out), err
}

// startRcloneGUI (re)launches the GUI on the engine's docker network (no host port).
func startRcloneGUI() error {
	hostDir, img, net, err := engineEnv()
	if err != nil {
		return err
	}
	_ = forceRemove(rgName)

	// No -p: reachable only inside `net` (by container name), never on the host.
	// --rc-no-auth: the GUI auto-connects (no rclone login form); access is gated
	// by the engine's session-authenticated reverse proxy + the isolated network.
	args := []string{
		"run", "-d", "--name", rgName, "--restart", "no", "--network", net,
		"--mount", "type=bind,source=" + hostDir + ",destination=/etc/rclone",
		"--entrypoint", "rclone", img,
		"rcd", "--rc-web-gui", "--rc-web-gui-no-open-browser", "--rc-no-auth",
		"--rc-baseurl", "/rclone-gui/",
		"--rc-addr", "0.0.0.0:5572",
		"--config", "/etc/rclone/rclone.conf",
	}
	if out, err := exec.Command("docker", args...).CombinedOutput(); err != nil {
		_ = forceRemove(rgName)
		return fmt.Errorf("기동 실패: %v: %s", err, strings.TrimSpace(string(out)))
	}

	rgMu.Lock()
	rgActive = true
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
	log.Printf("rclone-gui started on docker network %s (proxied at /rclone-gui/, auto-stop in %s)", net, rgTTL)
	return nil
}

func stopRcloneGUI() error {
	rgMu.Lock()
	if rgTimer != nil {
		rgTimer.Stop()
		rgTimer = nil
	}
	rgActive = false
	rgMu.Unlock()
	return forceRemove(rgName)
}

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
