package main

import (
	"log/slog"
	"maps"
	"path/filepath"
	"strings"
	"testing"
)

const goodToken = "a-token-that-is-long-enough-to-pass"

// setEnv installs a complete environment, then applies overrides. An empty
// override value unsets the variable.
func setEnv(t *testing.T, overrides map[string]string) {
	t.Helper()

	env := map[string]string{
		"ARTIFACT_TOKEN":           goodToken,
		"ARTIFACT_BASE_URL":        "https://artifact.example.com",
		"ARTIFACT_ADDR":            "",
		"ARTIFACT_DATA_DIR":        "",
		"ARTIFACT_DB":              "",
		"ARTIFACT_MAX_UPLOAD_SIZE": "",
		"ARTIFACT_LOG_LEVEL":       "",
		"PORT":                     "",
	}

	maps.Copy(env, overrides)

	for key, value := range env {
		t.Setenv(key, value)
	}
}

func TestLoadConfigDefaults(t *testing.T) {
	setEnv(t, nil)

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}

	if cfg.addr != ":8080" {
		t.Errorf("addr = %q, want :8080", cfg.addr)
	}

	if cfg.dataDir != "/data" {
		t.Errorf("dataDir = %q, want /data", cfg.dataDir)
	}

	if cfg.dbPath != filepath.Join("/data", "artifact.db") {
		t.Errorf("dbPath = %q, want /data/artifact.db", cfg.dbPath)
	}

	if cfg.maxUpload != defaultMaxUpload {
		t.Errorf("maxUpload = %d, want %d", cfg.maxUpload, defaultMaxUpload)
	}

	if cfg.logLevel != slog.LevelInfo {
		t.Errorf("logLevel = %v, want info", cfg.logLevel)
	}
}

func TestLoadConfigOverrides(t *testing.T) {
	setEnv(t, map[string]string{
		"ARTIFACT_ADDR":            "127.0.0.1:9000",
		"ARTIFACT_DATA_DIR":        "/srv/artifact",
		"ARTIFACT_MAX_UPLOAD_SIZE": "1024",
		"ARTIFACT_LOG_LEVEL":       "debug",
		"ARTIFACT_BASE_URL":        "https://files.example.com/",
	})

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}

	if cfg.addr != "127.0.0.1:9000" {
		t.Errorf("addr = %q", cfg.addr)
	}

	if cfg.dbPath != "/srv/artifact/artifact.db" {
		t.Errorf("dbPath = %q", cfg.dbPath)
	}

	if cfg.maxUpload != 1024 {
		t.Errorf("maxUpload = %d", cfg.maxUpload)
	}

	if cfg.logLevel != slog.LevelDebug {
		t.Errorf("logLevel = %v", cfg.logLevel)
	}

	// A trailing slash must not survive into every generated URL.
	if cfg.baseURL != "https://files.example.com" {
		t.Errorf("baseURL = %q, want the trailing slash trimmed", cfg.baseURL)
	}
}

func TestLoadConfigPort(t *testing.T) {
	setEnv(t, map[string]string{"PORT": "3000"})

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}

	if cfg.addr != ":3000" {
		t.Errorf("addr = %q, want :3000 from PORT", cfg.addr)
	}

	setEnv(t, map[string]string{"PORT": "3000", "ARTIFACT_ADDR": "127.0.0.1:9000"})

	if cfg, err = loadConfig(); err != nil {
		t.Fatalf("loadConfig: %v", err)
	}

	if cfg.addr != "127.0.0.1:9000" {
		t.Errorf("addr = %q, want ARTIFACT_ADDR to win over PORT", cfg.addr)
	}
}

func TestLoadConfigRejectsBadEnvironment(t *testing.T) {
	cases := map[string]struct {
		overrides map[string]string
		mentions  string
	}{
		"missing token": {
			overrides: map[string]string{"ARTIFACT_TOKEN": ""},
			mentions:  "ARTIFACT_TOKEN",
		},
		"short token": {
			overrides: map[string]string{"ARTIFACT_TOKEN": strings.Repeat("a", minTokenLength-1)},
			mentions:  "ARTIFACT_TOKEN",
		},
		"missing base url": {
			overrides: map[string]string{"ARTIFACT_BASE_URL": ""},
			mentions:  "ARTIFACT_BASE_URL",
		},
		"relative base url": {
			overrides: map[string]string{"ARTIFACT_BASE_URL": "artifact.example.com"},
			mentions:  "ARTIFACT_BASE_URL",
		},
		"non-http base url": {
			overrides: map[string]string{"ARTIFACT_BASE_URL": "ftp://artifact.example.com"},
			mentions:  "ARTIFACT_BASE_URL",
		},
		"non-numeric max upload": {
			overrides: map[string]string{"ARTIFACT_MAX_UPLOAD_SIZE": "100MB"},
			mentions:  "ARTIFACT_MAX_UPLOAD_SIZE",
		},
		"zero max upload": {
			overrides: map[string]string{"ARTIFACT_MAX_UPLOAD_SIZE": "0"},
			mentions:  "ARTIFACT_MAX_UPLOAD_SIZE",
		},
		"non-numeric port": {
			overrides: map[string]string{"PORT": "http"},
			mentions:  "PORT",
		},
		"unknown log level": {
			overrides: map[string]string{"ARTIFACT_LOG_LEVEL": "chatty"},
			mentions:  "ARTIFACT_LOG_LEVEL",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			setEnv(t, tc.overrides)

			cfg, err := loadConfig()
			if err == nil {
				t.Fatalf("loadConfig succeeded with %+v, want an error", cfg)
			}

			if !strings.Contains(err.Error(), tc.mentions) {
				t.Errorf("error %q does not name %s", err, tc.mentions)
			}
		})
	}
}

func TestLoadConfigTokenAtMinimumLength(t *testing.T) {
	setEnv(t, map[string]string{"ARTIFACT_TOKEN": strings.Repeat("a", minTokenLength)})

	if _, err := loadConfig(); err != nil {
		t.Errorf("a %d-character token was rejected: %v", minTokenLength, err)
	}
}
