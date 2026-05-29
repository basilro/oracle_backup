package main

import (
	"context"
	"crypto/rand"
	"embed"
	"encoding/hex"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/robfig/cron/v3"
)

//go:embed ui
var embeddedUI embed.FS

func uiFS() fs.FS { sub, _ := fs.Sub(embeddedUI, "ui"); return sub }

func randToken() string {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		log.Fatalf("crypto/rand failed: %v", err)
	}
	return hex.EncodeToString(b)
}

var (
	schedMu   sync.Mutex
	schedNext time.Time
)

func nextRun() string {
	schedMu.Lock()
	defer schedMu.Unlock()
	if schedNext.IsZero() {
		return ""
	}
	return schedNext.In(time.Local).Format("2006-01-02 15:04:05 MST")
}

func loadSessionKey() []byte {
	if v := os.Getenv("WEB_SESSION_KEY"); len(v) >= 32 {
		return []byte(v)
	}
	p := "/state/session.key"
	if b, err := os.ReadFile(p); err == nil && len(b) >= 32 {
		return b
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		log.Fatal("rand")
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, b, 0600); err != nil {
		log.Fatalf("session key not writable (fail closed): %v", err)
	}
	if err := os.Rename(tmp, p); err != nil {
		log.Fatalf("session key rename: %v", err)
	}
	return b
}

func trimSpace(b []byte) string {
	for len(b) > 0 && (b[len(b)-1] == '\n' || b[len(b)-1] == '\r' || b[len(b)-1] == ' ') {
		b = b[:len(b)-1]
	}
	return string(b)
}

func main() {
	cfgPath := "/config/config.env"
	store, err := OpenStore("/state/history.db")
	if err != nil {
		log.Fatal(err)
	}
	defer store.Close()
	store.ReconcileStuck()

	runner := NewRunner(store, "/opt/backup/scripts", "/var/log/backup")

	adminUser := os.Getenv("WEB_ADMIN_USER")
	if adminUser == "" {
		adminUser = "admin"
	}
	adminHash := ""
	if b, err := os.ReadFile("/secrets/web-admin.hash"); err == nil {
		adminHash = trimSpace(b)
	} else {
		log.Println("WARNING: /secrets/web-admin.hash missing — login disabled until provided")
	}

	// appCtx is threaded into every backup/restore so an in-flight restic run
	// is cancelled (SIGINT to its process group) on shutdown, releasing its lock.
	appCtx, appCancel := context.WithCancel(context.Background())

	srv := &Server{
		store: store, runner: runner, cfgPath: cfgPath, logDir: "/var/log/backup",
		sessionKey: loadSessionKey(), adminUser: adminUser, adminHash: adminHash,
		restoreRoot: "/restore-out", appCtx: appCtx,
	}

	var c *cron.Cron = cron.New(cron.WithLocation(time.Local))
	reload := func() {
		schedMu.Lock()
		defer schedMu.Unlock()
		c.Stop()
		nc := cron.New(cron.WithLocation(time.Local))
		cfg, err := LoadConfig(cfgPath)
		if err == nil && cfg.SchedulerOn && cfg.BackupSchedule != "" {
			if _, e := nc.AddFunc(cfg.BackupSchedule, func() { runner.RunBackup(appCtx, "scheduled") }); e != nil {
				log.Printf("bad BACKUP_SCHEDULE: %v", e)
			}
			if cfg.CheckSchedule != "" {
				if _, e := nc.AddFunc(cfg.CheckSchedule, func() { runner.RunCheck(appCtx) }); e != nil {
					log.Printf("bad CHECK_SCHEDULE: %v", e)
				}
			}
			nc.Start()
			if ents := nc.Entries(); len(ents) > 0 {
				schedNext = ents[0].Next
			}
			log.Printf("scheduler ON next=%s", schedNext)
		} else {
			schedNext = time.Time{}
			log.Printf("scheduler OFF")
		}
		c = nc
	}
	srv.reload = reload
	reload()

	httpSrv := &http.Server{Addr: ":8088", Handler: srv.Routes()}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	go func() {
		log.Printf("listening on :8088 (admin=%s)", adminUser)
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatal(err)
		}
	}()
	<-ctx.Done()
	log.Println("shutting down...")
	appCancel() // signal any in-flight restic run to stop cleanly
	schedMu.Lock()
	c.Stop()
	schedMu.Unlock()
	sctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	httpSrv.Shutdown(sctx)
}
