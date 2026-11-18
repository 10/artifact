package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/10/artifact/internal/api"
	"github.com/10/artifact/internal/store"
)

const cliToken = "cli-test-token-0123456789-abcdefgh"

// cliEnv is a real server plus an isolated HOME, so no part of the developer's
// own configuration can leak into a test.
type cliEnv struct {
	t      *testing.T
	server *httptest.Server
	home   string
}

func newCLI(t *testing.T) *cliEnv {
	t.Helper()

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ARTIFACT_ENDPOINT", "")
	t.Setenv("ARTIFACT_TOKEN", "")
	t.Setenv("ARTIFACT_CONFIG", "")

	dataDir := t.TempDir()

	st, err := store.Open(dataDir, filepath.Join(dataDir, "artifact.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}

	t.Cleanup(func() { st.Close() })

	server := httptest.NewServer(api.New(st, api.Config{
		Token:         cliToken,
		BaseURL:       "https://artifact.example.com",
		MaxUploadSize: 1 << 20,
		Logger:        slog.New(slog.DiscardHandler),
	}))
	t.Cleanup(server.Close)

	return &cliEnv{t: t, server: server, home: home}
}

func (c *cliEnv) run(args ...string) (int, string, string) {
	c.t.Helper()

	var stdout, stderr bytes.Buffer

	code := run(c.t.Context(), args, &stdout, &stderr)

	return code, stdout.String(), stderr.String()
}

// connected prefixes args with a working endpoint and token.
func (c *cliEnv) connected(args ...string) []string {
	return append([]string{"--endpoint", c.server.URL, "--token", cliToken}, args...)
}

func (c *cliEnv) writeFile(name, body string) string {
	c.t.Helper()

	path := filepath.Join(c.t.TempDir(), name)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		c.t.Fatalf("write %s: %v", name, err)
	}

	return path
}

func (c *cliEnv) writeConfig(mode os.FileMode, body string) string {
	c.t.Helper()

	dir := filepath.Join(c.home, ".config", "artifact")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		c.t.Fatalf("mkdir config: %v", err)
	}

	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		c.t.Fatalf("write config: %v", err)
	}

	return path
}

// upload publishes a file and returns the URL the CLI printed.
func (c *cliEnv) upload(name, body string) string {
	c.t.Helper()

	code, stdout, stderr := c.run(c.connected(c.writeFile(name, body))...)
	if code != exitOK {
		c.t.Fatalf("upload %s: exit %d, stderr %q", name, code, stderr)
	}

	return strings.TrimSpace(stdout)
}

// ---------------------------------------------------------- output contract

func TestUpPrintsOnlyTheURL(t *testing.T) {
	c := newCLI(t)
	file := c.writeFile("report.html", "<h1>hi</h1>")

	code, stdout, stderr := c.run(c.connected(file)...)

	if code != exitOK {
		t.Fatalf("exit = %d, want 0 (stderr %q)", code, stderr)
	}

	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}

	lines := strings.Split(strings.TrimSuffix(stdout, "\n"), "\n")
	if len(lines) != 1 {
		t.Fatalf("stdout has %d lines, want exactly 1: %q", len(lines), stdout)
	}

	if !strings.HasPrefix(lines[0], "https://artifact.example.com/") {
		t.Errorf("stdout = %q, want just a URL", stdout)
	}

	if !strings.HasSuffix(stdout, "\n") {
		t.Error("stdout is not newline-terminated")
	}
}

func TestUpSubcommandFormMatchesBareForm(t *testing.T) {
	c := newCLI(t)
	file := c.writeFile("plan.md", "# plan")

	code, stdout, stderr := c.run(c.connected("up", "--slug", "daily-report", file)...)
	if code != exitOK {
		t.Fatalf("exit = %d, want 0 (stderr %q)", code, stderr)
	}

	if got := strings.TrimSpace(stdout); got != "https://artifact.example.com/daily-report" {
		t.Errorf("stdout = %q, want the slug URL", got)
	}
}

func TestUpPublishesStandardInput(t *testing.T) {
	c := newCLI(t)

	const log = "ok  pkg/a\nFAIL pkg/b\n"

	stdin, err := os.Open(c.writeFile("piped", log))
	if err != nil {
		t.Fatalf("open stdin file: %v", err)
	}
	defer stdin.Close()

	saved := os.Stdin
	os.Stdin = stdin

	t.Cleanup(func() { os.Stdin = saved })

	code, stdout, stderr := c.run(c.connected("--json", "--filename", "test.log", "-")...)
	if code != exitOK {
		t.Fatalf("exit = %d, want 0 (stderr %q)", code, stderr)
	}

	var resp struct {
		Filename    string `json:"filename"`
		ContentType string `json:"content_type"`
		Size        int64  `json:"size"`
	}

	if err := json.Unmarshal([]byte(stdout), &resp); err != nil {
		t.Fatalf("decode %q: %v", stdout, err)
	}

	if resp.Filename != "test.log" || resp.Size != int64(len(log)) || resp.ContentType != "text/plain; charset=utf-8" {
		t.Errorf("published %+v, want test.log, %d bytes, text/plain", resp, len(log))
	}
}

// A piped log can carry secrets, so no copy of it may sit on disk under a
// name, even while the input is still arriving.
func TestStandardInputLeavesNothingOnDisk(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows cannot unlink an open file")
	}

	c := newCLI(t)
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}

	saved := os.Stdin
	os.Stdin = r

	t.Cleanup(func() {
		os.Stdin = saved
		r.Close()
	})

	done := make(chan int, 1)

	go func() {
		code, _, _ := c.run(c.connected("--filename", "build.log", "-")...)
		done <- code
	}()

	if _, err := w.WriteString("partial log\n"); err != nil {
		t.Fatalf("write stdin: %v", err)
	}

	for range 30 {
		if entries, _ := os.ReadDir(tmp); len(entries) > 0 {
			t.Fatalf("input is on disk while still arriving: %s", entries[0].Name())
		}

		time.Sleep(10 * time.Millisecond)
	}

	w.Close()

	if code := <-done; code != exitOK {
		t.Fatalf("exit = %d, want 0", code)
	}
}

func TestJSONEmitsTheResponseObjectAndNothingElse(t *testing.T) {
	c := newCLI(t)
	file := c.writeFile("report.html", "<h1>hi</h1>")

	code, stdout, stderr := c.run(append(c.connected(), "--json", file)...)
	if code != exitOK {
		t.Fatalf("exit = %d, want 0 (stderr %q)", code, stderr)
	}

	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}

	var payload map[string]any
	if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
		t.Fatalf("stdout is not a single JSON object: %v (%q)", err, stdout)
	}

	for _, key := range []string{"id", "url", "raw_url", "version", "sha256", "content_type"} {
		if _, ok := payload[key]; !ok {
			t.Errorf("response is missing %q", key)
		}
	}
}

func TestQuietSuppressesStderr(t *testing.T) {
	c := newCLI(t)

	code, stdout, stderr := c.run(append(c.connected(), "--quiet", "/no/such/file.txt")...)

	if code != exitError {
		t.Errorf("exit = %d, want %d", code, exitError)
	}

	if stderr != "" {
		t.Errorf("stderr = %q, want empty under --quiet", stderr)
	}

	if stdout != "" {
		t.Errorf("stdout = %q, want empty", stdout)
	}
}

func TestRmWritesNothingToStdout(t *testing.T) {
	c := newCLI(t)

	url := c.upload("notes.txt", "hello")
	id := url[strings.LastIndex(url, "/")+1:]

	code, stdout, stderr := c.run(c.connected("rm", id)...)
	if code != exitOK {
		t.Fatalf("exit = %d, want 0 (stderr %q)", code, stderr)
	}

	if stdout != "" {
		t.Errorf("stdout = %q, want empty", stdout)
	}

	if !strings.Contains(stderr, "deleted "+id) {
		t.Errorf("stderr = %q, want a deletion notice", stderr)
	}
}

func TestListAndInfoPrintTables(t *testing.T) {
	c := newCLI(t)

	url := c.upload("notes.txt", "hello")
	id := url[strings.LastIndex(url, "/")+1:]

	code, stdout, stderr := c.run(c.connected("list", "--limit", "5")...)
	if code != exitOK {
		t.Fatalf("list: exit %d (stderr %q)", code, stderr)
	}

	if !strings.HasPrefix(stdout, "ID") || !strings.Contains(stdout, id) {
		t.Errorf("list output = %q, want a table containing %q", stdout, id)
	}

	code, stdout, stderr = c.run(c.connected("info", id)...)
	if code != exitOK {
		t.Fatalf("info: exit %d (stderr %q)", code, stderr)
	}

	for _, want := range []string{"ID", "URL", "Latest", "VER", "notes.txt"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("info output is missing %q:\n%s", want, stdout)
		}
	}
}

func TestUpdateKeepsTheURL(t *testing.T) {
	c := newCLI(t)

	first := c.upload("plan.md", "# v1")
	id := first[strings.LastIndex(first, "/")+1:]

	code, stdout, stderr := c.run(c.connected("update", id, c.writeFile("plan.md", "# v2"))...)
	if code != exitOK {
		t.Fatalf("exit = %d, want 0 (stderr %q)", code, stderr)
	}

	if got := strings.TrimSpace(stdout); got != first {
		t.Errorf("url = %q, want it unchanged at %q", got, first)
	}
}

func TestVersionNeedsNoConfiguration(t *testing.T) {
	c := newCLI(t)

	code, stdout, stderr := c.run("version")
	if code != exitOK {
		t.Fatalf("exit = %d, want 0 (stderr %q)", code, stderr)
	}

	if !strings.HasPrefix(stdout, "artifact ") {
		t.Errorf("stdout = %q, want version output", stdout)
	}
}

// ---------------------------------------------------------------- exit codes

func TestExitCodes(t *testing.T) {
	cases := []struct {
		name string
		args func(c *cliEnv) []string
		want int
	}{
		{
			name: "success",
			args: func(c *cliEnv) []string { return c.connected(c.writeFile("a.txt", "data")) },
			want: exitOK,
		},
		{
			name: "unreadable file",
			args: func(c *cliEnv) []string { return c.connected("/no/such/file.txt") },
			want: exitError,
		},
		{
			name: "standard input without a filename",
			args: func(c *cliEnv) []string { return c.connected("-") },
			want: exitUsage,
		},
		{
			name: "unknown flag",
			args: func(c *cliEnv) []string { return c.connected("--nope") },
			want: exitUsage,
		},
		{
			name: "unknown subcommand argument count",
			args: func(c *cliEnv) []string { return c.connected("update") },
			want: exitUsage,
		},
		{
			name: "no configuration at all",
			args: func(c *cliEnv) []string { return []string{c.writeFile("a.txt", "data")} },
			want: exitUsage,
		},
		{
			name: "wrong token",
			args: func(c *cliEnv) []string {
				return []string{"--endpoint", c.server.URL, "--token", "wrong-token", c.writeFile("a.txt", "data")}
			},
			want: exitAuth,
		},
		{
			name: "artifact not found",
			args: func(c *cliEnv) []string { return c.connected("info", "zzzzzzzz") },
			want: exitNotFound,
		},
		{
			name: "delete missing artifact",
			args: func(c *cliEnv) []string { return c.connected("rm", "zzzzzzzz") },
			want: exitNotFound,
		},
		{
			name: "unreachable server",
			args: func(c *cliEnv) []string {
				return []string{"--endpoint", "http://127.0.0.1:1", "--token", cliToken, c.writeFile("a.txt", "d")}
			},
			want: exitError,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := newCLI(t)

			code, _, stderr := c.run(tc.args(c)...)
			if code != tc.want {
				t.Errorf("exit = %d, want %d (stderr %q)", code, tc.want, stderr)
			}
		})
	}
}

// The one line on stderr is what an agent reports, so it should name the
// missing file once rather than repeat the path.
func TestMissingFileIsNamedOnce(t *testing.T) {
	c := newCLI(t)
	missing := filepath.Join(t.TempDir(), "absent.png")

	code, stdout, stderr := c.run(c.connected(missing)...)
	if code != exitError || stdout != "" {
		t.Fatalf("exit = %d, stdout = %q; want 1 and nothing", code, stdout)
	}

	if n := strings.Count(stderr, missing); n != 1 {
		t.Errorf("stderr names the path %d times, want once: %q", n, stderr)
	}
}

// ---------------------------------------------------------------- precedence

func TestConfigFileSuppliesEndpointAndToken(t *testing.T) {
	c := newCLI(t)
	c.writeConfig(0o600, fmt.Sprintf("# comment\nendpoint = %q\ntoken    = %q\n", c.server.URL, cliToken))

	code, stdout, stderr := c.run(c.writeFile("a.txt", "data"))
	if code != exitOK {
		t.Fatalf("exit = %d, want 0 (stderr %q)", code, stderr)
	}

	if !strings.HasPrefix(stdout, "https://artifact.example.com/") {
		t.Errorf("stdout = %q", stdout)
	}
}

func TestEnvironmentBeatsConfigFile(t *testing.T) {
	c := newCLI(t)
	c.writeConfig(0o600, "endpoint = \"http://127.0.0.1:1\"\ntoken = \"wrong-token\"\n")

	t.Setenv("ARTIFACT_ENDPOINT", c.server.URL)
	t.Setenv("ARTIFACT_TOKEN", cliToken)

	code, _, stderr := c.run(c.writeFile("a.txt", "data"))
	if code != exitOK {
		t.Fatalf("exit = %d, want 0 (stderr %q)", code, stderr)
	}
}

func TestFlagBeatsEnvironment(t *testing.T) {
	c := newCLI(t)

	t.Setenv("ARTIFACT_ENDPOINT", "http://127.0.0.1:1")
	t.Setenv("ARTIFACT_TOKEN", "wrong-token")

	code, _, stderr := c.run(c.connected(c.writeFile("a.txt", "data"))...)
	if code != exitOK {
		t.Fatalf("exit = %d, want 0 (stderr %q)", code, stderr)
	}
}

func TestConfigFileMixesWithEnvironment(t *testing.T) {
	c := newCLI(t)
	c.writeConfig(0o600, fmt.Sprintf("token = %q\n", cliToken))

	t.Setenv("ARTIFACT_ENDPOINT", c.server.URL)

	code, _, stderr := c.run(c.writeFile("a.txt", "data"))
	if code != exitOK {
		t.Fatalf("exit = %d, want 0 (stderr %q)", code, stderr)
	}
}

func TestLooseConfigPermissionsWarn(t *testing.T) {
	c := newCLI(t)
	path := c.writeConfig(0o644, fmt.Sprintf("endpoint = %q\ntoken = %q\n", c.server.URL, cliToken))

	code, _, stderr := c.run(c.writeFile("a.txt", "data"))
	if code != exitOK {
		t.Fatalf("exit = %d, want 0 (stderr %q)", code, stderr)
	}

	if !strings.Contains(stderr, "chmod 600") || !strings.Contains(stderr, path) {
		t.Errorf("stderr = %q, want a permissions warning naming %s", stderr, path)
	}
}

func TestTightConfigPermissionsAreSilent(t *testing.T) {
	c := newCLI(t)
	c.writeConfig(0o600, fmt.Sprintf("endpoint = %q\ntoken = %q\n", c.server.URL, cliToken))

	code, _, stderr := c.run(c.writeFile("a.txt", "data"))
	if code != exitOK {
		t.Fatalf("exit = %d, want 0 (stderr %q)", code, stderr)
	}

	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}
}

func TestExplicitConfigFlag(t *testing.T) {
	c := newCLI(t)

	path := filepath.Join(t.TempDir(), "elsewhere.toml")
	body := fmt.Sprintf("endpoint = %q\ntoken = %q\n", c.server.URL, cliToken)

	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	code, _, stderr := c.run("--config", path, c.writeFile("a.txt", "data"))
	if code != exitOK {
		t.Fatalf("exit = %d, want 0 (stderr %q)", code, stderr)
	}
}

func TestParseConfig(t *testing.T) {
	t.Parallel()

	got := parseConfig(`
# a comment
endpoint = "https://example.com"
token    = 'single-quoted'
ignored  = "value"
malformed line
`)

	if got.endpoint != "https://example.com" {
		t.Errorf("endpoint = %q", got.endpoint)
	}

	if got.token != "single-quoted" {
		t.Errorf("token = %q", got.token)
	}
}

func TestHumanSize(t *testing.T) {
	t.Parallel()

	cases := map[int64]string{
		0:       "0 B",
		512:     "512 B",
		1024:    "1.0 KiB",
		20481:   "20.0 KiB",
		1 << 20: "1.0 MiB",
		1 << 30: "1.0 GiB",
	}

	for size, want := range cases {
		if got := humanSize(size); got != want {
			t.Errorf("humanSize(%d) = %q, want %q", size, got, want)
		}
	}
}
