package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDotEnvPreservesProcessEnvironment(t *testing.T) {
	t.Setenv("ARL_TEST_EXISTING", "process")
	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(path, []byte("ARL_TEST_EXISTING=file\nARL_TEST_NEW='value'\n# comment\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ARL_TEST_NEW", "")
	os.Unsetenv("ARL_TEST_NEW")
	if err := DotEnv(path); err != nil {
		t.Fatal(err)
	}
	defer os.Unsetenv("ARL_TEST_NEW")
	if os.Getenv("ARL_TEST_EXISTING") != "process" || os.Getenv("ARL_TEST_NEW") != "value" {
		t.Fatal("dotenv precedence")
	}
}
func TestInvalidDatabaseDoesNotLeakCredentials(t *testing.T) {
	t.Setenv("NEON_DB_URL", "not-a-url-with-SECRET")
	_, err := Load("sync")
	if err == nil || err.Error() == "not-a-url-with-SECRET" {
		t.Fatal("invalid DSN accepted/leaked")
	}
}
func TestPoolsAndConcurrencyAreBounded(t *testing.T) {
	t.Setenv("NEON_DB_URL", "postgres://user:password@localhost/test?sslmode=disable")
	t.Setenv("ARL_DB_MAX_CONNS", "2000")
	t.Setenv("ARL_WORKER_CONCURRENCY", "1000")
	c, err := Load("async")
	if err != nil || c.DBMax != 16 || c.Concurrency != 32 {
		t.Fatal(c, err)
	}
}
func TestFeaturesFromEnvironment(t *testing.T) {
	t.Setenv("NEON_DB_URL", "postgres://user:password@localhost/test?sslmode=disable")
	t.Setenv("ARL_FEATURES", "tracing,-otel")
	c, err := Load("sync")
	if err != nil || !c.Features.On("traces") || c.Features.On("otel") || c.Features.On("experiments") {
		t.Fatal(c.Features.Enabled(), err)
	}
	t.Setenv("ARL_FEATURES", "traces,bogus")
	if _, err = Load("sync"); err == nil {
		t.Fatal("unknown feature accepted")
	}
}
