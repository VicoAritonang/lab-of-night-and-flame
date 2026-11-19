package config

import (
	"bufio"
	"errors"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"go.vicoworks.com/agent-reliability-lab/internal/features"
)

type Config struct {
	DatabaseURL, Address, AuthToken, QueueURL, Region, Mode, LocalURL string
	Origins                                                           []string
	DBMax, Concurrency, MaxHTTP, MaxWebSockets                        int
	JobLease, JobWait                                                 time.Duration
	// All-in-one development mode: apply the schema, run the job worker in
	// process, accept agents without pressing "Start listening" and serve the UI.
	AutoMigrate, EmbeddedWorker, ListenOnStart, ServeUI bool
	// Features selects the optional modules (ARL_FEATURES, default all); the
	// zero value enables every compiled module. FeatureNotes explains implicit
	// changes made while resolving it.
	Features     features.Set
	FeatureNotes []string
}

func DotEnv(path string) error {
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()
	s := bufio.NewScanner(f)
	s.Buffer(make([]byte, 4096), 1024*1024)
	for s.Scan() {
		line := strings.TrimSpace(s.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		v = strings.Trim(strings.TrimSpace(v), "\"'")
		if _, ok := os.LookupEnv(k); !ok {
			_ = os.Setenv(k, v)
		}
	}
	return s.Err()
}
func Env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
func Integer(k string, def int) int {
	n, e := strconv.Atoi(os.Getenv(k))
	if e != nil || n < 1 {
		return def
	}
	return n
}
func Flag(k string, def bool) bool {
	switch strings.ToLower(os.Getenv(k)) {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	}
	return def
}
func Load(mode string) (Config, error) {
	if err := DotEnv(".env"); err != nil {
		return Config{}, err
	}
	maxDB := 4
	if mode == "async" {
		maxDB = 2
	}
	c := Config{DatabaseURL: Env("ARL_DATABASE_URL", os.Getenv("NEON_DB_URL")), Address: Env("ARL_ADDR", ":8000"), AuthToken: os.Getenv("ARL_AUTH_TOKEN"), QueueURL: os.Getenv("ARL_SQS_QUEUE_URL"), Region: Env("AWS_REGION", "ap-southeast-1"), Mode: mode, LocalURL: Env("ARL_LOCAL_BASE_URL", "http://localhost:11434/v1"), Origins: strings.FieldsFunc(Env("ARL_ALLOWED_ORIGINS", "http://localhost:5173,http://127.0.0.1:5173"), func(r rune) bool { return r == ',' }), DBMax: Integer("ARL_DB_MAX_CONNS", maxDB), Concurrency: Integer("ARL_WORKER_CONCURRENCY", 4), MaxHTTP: Integer("ARL_MAX_HTTP", 48), MaxWebSockets: Integer("ARL_MAX_WS", 100), JobLease: 12 * time.Minute, JobWait: 110 * time.Second}
	c.QueueURL = strings.TrimSpace(c.QueueURL)
	c.AutoMigrate = Flag("ARL_AUTO_MIGRATE", false)
	c.EmbeddedWorker = Flag("ARL_EMBEDDED_WORKER", c.QueueURL == "")
	c.ListenOnStart = Flag("ARL_LISTEN_ON_START", false)
	c.ServeUI = Flag("ARL_SERVE_UI", true)
	set, notes, err := features.Parse(os.Getenv("ARL_FEATURES"))
	if err != nil {
		return c, err
	}
	c.Features, c.FeatureNotes = set, notes
	if c.Concurrency > 32 {
		c.Concurrency = 32
	}
	if c.DBMax > 16 {
		c.DBMax = 16
	}
	if c.DatabaseURL == "" {
		return c, errors.New("ARL_DATABASE_URL (or NEON_DB_URL) is required")
	}
	u, err := url.Parse(c.DatabaseURL)
	if err != nil || u.Host == "" || (u.Scheme != "postgres" && u.Scheme != "postgresql") {
		return c, errors.New("ARL_DATABASE_URL must be a valid PostgreSQL connection URL")
	}
	return c, nil
}
