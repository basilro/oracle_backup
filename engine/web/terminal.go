package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/creack/pty"
	"github.com/gorilla/websocket"
)

// Web terminal: bridges an xterm.js terminal (browser) over a WebSocket to an
// interactive `rclone config` running in a one-off PTY'd container (rw ./rclone
// mount). Scope is rclone config ONLY (not a shell) — the wizard has no option
// to run arbitrary commands or exfiltrate the token, so internet is kept (needed
// for Drive OAuth + connection tests) while the container is otherwise locked
// down (no-new-privileges, cap-drop, pids/mem/cpu caps). Session-auth +
// same-origin gated, concurrency-capped, idle + hard TTL, auto-cleanup.
const (
	termTTL     = 30 * time.Minute
	termIdle    = 15 * time.Minute
	termMaxConc = 4
)

// termSlots caps concurrent terminal sessions (one container each).
var termSlots = make(chan struct{}, termMaxConc)

var wsUpgrade = websocket.Upgrader{
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
	CheckOrigin: func(r *http.Request) bool {
		o := r.Header.Get("Origin")
		if o == "" {
			return false
		}
		u, err := url.Parse(o)
		return err == nil && u.Host == r.Host
	},
}

// reconcileTermContainers force-removes any leftover terminal containers from a
// prior engine run (called on boot).
func reconcileTermContainers() {
	out, err := exec.Command("docker", "ps", "-aq", "--filter", "name=backupstack_term_").Output()
	if err != nil {
		return
	}
	for _, id := range strings.Fields(string(out)) {
		exec.Command("docker", "rm", "-f", id).Run()
	}
}

func (s *Server) handleTerminalWS(w http.ResponseWriter, r *http.Request) {
	hostDir, img, net, err := engineEnv()
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	conn, err := wsUpgrade.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	// concurrency cap
	select {
	case termSlots <- struct{}{}:
		defer func() { <-termSlots }()
	default:
		conn.WriteMessage(websocket.BinaryMessage, []byte("동시 터미널 세션 한도(4) 초과 — 잠시 후 다시 시도하세요\r\n"))
		return
	}

	name := "backupstack_term_" + strings.ToLower(randPassword(10))
	_ = forceRemove(name)
	defer exec.Command("docker", "rm", "-f", name).Run()

	ctx, cancel := context.WithTimeout(context.Background(), termTTL)
	defer cancel()
	cmd := exec.CommandContext(ctx, "docker", "run", "--rm", "-i", "-t", "--name", name,
		"--network", net,
		"--security-opt", "no-new-privileges:true",
		"--cap-drop", "ALL",
		"--pids-limit", "256", "--memory", "512m", "--cpus", "1",
		"--mount", "type=bind,source="+hostDir+",destination=/etc/rclone",
		"-e", "RCLONE_CONFIG=/etc/rclone/rclone.conf",
		"--entrypoint", "rclone", img, "config")
	ptmx, err := pty.Start(cmd)
	if err != nil {
		conn.WriteMessage(websocket.BinaryMessage, []byte("터미널 시작 실패: "+err.Error()))
		return
	}
	defer ptmx.Close()
	user, _ := s.currentUser(r)
	s.store.Audit(user, "terminal", "open")

	conn.SetReadLimit(64 * 1024)
	conn.SetReadDeadline(time.Now().Add(termIdle))
	conn.SetPongHandler(func(string) error {
		conn.SetReadDeadline(time.Now().Add(termIdle))
		return nil
	})

	done := make(chan struct{})
	var once sync.Once
	stop := func() { once.Do(func() { close(done) }) }
	defer stop()

	// ping keepalive (proxies drop idle WS otherwise)
	go func() {
		t := time.NewTicker(30 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
				if conn.WriteMessage(websocket.PingMessage, nil) != nil {
					return
				}
			}
		}
	}()

	// PTY → WS
	go func() {
		defer stop()
		buf := make([]byte, 4096)
		for {
			n, rerr := ptmx.Read(buf)
			if n > 0 {
				conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
				if conn.WriteMessage(websocket.BinaryMessage, buf[:n]) != nil {
					return
				}
			}
			if rerr != nil {
				return
			}
		}
	}()

	// WS → PTY
	for {
		_, data, rerr := conn.ReadMessage()
		if rerr != nil {
			break
		}
		conn.SetReadDeadline(time.Now().Add(termIdle))
		var msg struct {
			Type, Data string
			Cols, Rows int
		}
		if json.Unmarshal(data, &msg) == nil && msg.Type != "" {
			switch msg.Type {
			case "input":
				ptmx.Write([]byte(msg.Data))
			case "resize":
				if msg.Cols > 0 && msg.Rows > 0 {
					pty.Setsize(ptmx, &pty.Winsize{Rows: uint16(msg.Rows), Cols: uint16(msg.Cols)})
				}
			}
		} else {
			ptmx.Write(data)
		}
	}
	s.store.Audit(user, "terminal", "close")
}
