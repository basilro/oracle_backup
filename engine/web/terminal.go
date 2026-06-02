package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"os/exec"
	"strings"
	"time"

	"github.com/creack/pty"
	"github.com/gorilla/websocket"
)

// Web terminal: bridges an xterm.js terminal (browser) over a WebSocket to an
// interactive `rclone config` running in a one-off PTY'd container (rw ./rclone
// mount). Scope is rclone config only (not a shell). Session-auth + same-origin
// gated; one container per session with a hard 30m cap.
const termTTL = 30 * time.Minute

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

func (s *Server) handleTerminalWS(w http.ResponseWriter, r *http.Request) {
	hostDir, img, _, err := engineEnv()
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	conn, err := wsUpgrade.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	name := "backupstack_term_" + strings.ToLower(randPassword(10))
	ctx, cancel := context.WithTimeout(context.Background(), termTTL)
	defer cancel()

	// -i -t for an interactive TTY; --rm + explicit rm on exit so nothing lingers.
	cmd := exec.CommandContext(ctx, "docker", "run", "--rm", "-i", "-t", "--name", name,
		"--mount", "type=bind,source="+hostDir+",destination=/etc/rclone",
		"-e", "RCLONE_CONFIG=/etc/rclone/rclone.conf",
		"--entrypoint", "rclone", img, "config")
	ptmx, err := pty.Start(cmd)
	if err != nil {
		conn.WriteMessage(websocket.BinaryMessage, []byte("터미널 시작 실패: "+err.Error()))
		return
	}
	defer func() {
		ptmx.Close()
		exec.Command("docker", "rm", "-f", name).Run()
	}()
	user, _ := s.currentUser(r)
	s.store.Audit(user, "terminal", "open")

	// PTY → WS (binary frames; xterm decodes bytes safely across multibyte splits)
	go func() {
		buf := make([]byte, 4096)
		for {
			n, rerr := ptmx.Read(buf)
			if n > 0 {
				if werr := conn.WriteMessage(websocket.BinaryMessage, buf[:n]); werr != nil {
					return
				}
			}
			if rerr != nil {
				conn.Close()
				return
			}
		}
	}()

	// WS → PTY (JSON control messages: input / resize)
	for {
		_, data, rerr := conn.ReadMessage()
		if rerr != nil {
			break
		}
		var msg struct {
			Type       string `json:"type"`
			Data       string `json:"data"`
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
	cancel()
	s.store.Audit(user, "terminal", "close")
}
