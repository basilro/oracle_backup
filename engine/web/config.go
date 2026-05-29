package main

import (
	"bufio"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/robfig/cron/v3"
)

type Config struct {
	KeepDaily       int
	UploadLimit     int
	BackupSchedule  string
	CheckSchedule   string
	SchedulerOn     bool
	DBBackupEnabled bool
	MinFreeMB       int
	PgContainer     string
	MongoContainer  string
	RedisContainer  string
	raw             map[string]string
	order           []string
}

var identRe = regexp.MustCompile(`^[A-Za-z0-9_.-]*$`)
var cronParser = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)

func LoadConfig(path string) (*Config, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	c := &Config{raw: map[string]string{}, SchedulerOn: true, DBBackupEnabled: true, MinFreeMB: 5000, KeepDaily: 7}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") || !strings.Contains(line, "=") {
			c.order = append(c.order, line)
			continue
		}
		eq := strings.Index(line, "=")
		k := strings.TrimSpace(line[:eq])
		v := strings.Trim(strings.TrimSpace(line[eq+1:]), `"'`)
		c.raw[k] = v
		c.order = append(c.order, k)
	}
	atoi := func(k string, d int) int {
		if s, ok := c.raw[k]; ok {
			if n, e := strconv.Atoi(s); e == nil {
				return n
			}
		}
		return d
	}
	c.KeepDaily = atoi("KEEP_DAILY", 7)
	c.UploadLimit = atoi("UPLOAD_LIMIT_KBPS", 0)
	c.MinFreeMB = atoi("DB_DUMP_MIN_FREE_MB", 5000)
	c.BackupSchedule = c.raw["BACKUP_SCHEDULE"]
	c.CheckSchedule = c.raw["CHECK_SCHEDULE"]
	c.SchedulerOn = c.raw["SCHEDULER_ENABLED"] != "false"
	c.DBBackupEnabled = c.raw["DB_BACKUP_ENABLED"] != "false"
	c.PgContainer = c.raw["PG_CONTAINER"]
	c.MongoContainer = c.raw["MONGO_CONTAINER"]
	c.RedisContainer = c.raw["REDIS_CONTAINER"]
	return c, nil
}

func (c *Config) Validate() error {
	if c.KeepDaily < 1 || c.KeepDaily > 3650 {
		return fmt.Errorf("KEEP_DAILY out of range")
	}
	if c.UploadLimit < 0 || c.UploadLimit > 10_000_000 {
		return fmt.Errorf("UPLOAD_LIMIT_KBPS out of range")
	}
	if c.MinFreeMB < 0 {
		return fmt.Errorf("DB_DUMP_MIN_FREE_MB negative")
	}
	if _, err := cronParser.Parse(c.BackupSchedule); err != nil {
		return fmt.Errorf("invalid BACKUP_SCHEDULE: %w", err)
	}
	if c.CheckSchedule != "" {
		if _, err := cronParser.Parse(c.CheckSchedule); err != nil {
			return fmt.Errorf("invalid CHECK_SCHEDULE: %w", err)
		}
	}
	for _, n := range []string{c.PgContainer, c.MongoContainer, c.RedisContainer} {
		if !identRe.MatchString(n) {
			return fmt.Errorf("invalid container name %q", n)
		}
	}
	return nil
}

// Save writes atomically (temp+rename), updating known keys, preserving comments/order.
func (c *Config) Save(path string) error {
	set := func(k, v string) {
		c.raw[k] = v
		for _, o := range c.order {
			if o == k {
				return
			}
		}
		c.order = append(c.order, k)
	}
	set("KEEP_DAILY", strconv.Itoa(c.KeepDaily))
	set("UPLOAD_LIMIT_KBPS", strconv.Itoa(c.UploadLimit))
	set("DB_DUMP_MIN_FREE_MB", strconv.Itoa(c.MinFreeMB))
	set("BACKUP_SCHEDULE", c.BackupSchedule)
	set("CHECK_SCHEDULE", c.CheckSchedule)
	set("SCHEDULER_ENABLED", boolStr(c.SchedulerOn))
	set("DB_BACKUP_ENABLED", boolStr(c.DBBackupEnabled))
	var b strings.Builder
	for _, o := range c.order {
		if v, ok := c.raw[o]; ok && o != "" && !strings.HasPrefix(o, "#") {
			fmt.Fprintf(&b, "%s=%s\n", o, v)
		} else {
			b.WriteString(o + "\n")
		}
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(b.String()), 0644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}
