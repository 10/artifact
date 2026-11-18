// Command artifact publishes a local file and prints its URL.
//
// Stdout carries the URL and nothing else. Progress, warnings, and errors all
// go to stderr, which is what makes `URL=$(artifact screenshot.png)` work.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"text/tabwriter"

	"github.com/alecthomas/kong"

	"github.com/10/artifact/internal/client"
)

// Build metadata, overwritten at build time with -ldflags.
var (
	Version = "dev"
	Commit  = "none"
	Date    = "unknown"
)

// Exit codes, per the CLI specification.
const (
	exitOK       = 0
	exitError    = 1
	exitUsage    = 2
	exitAuth     = 3
	exitNotFound = 4
)

// usageError is a misconfiguration the user must fix on the command line.
type usageError string

func (e usageError) Error() string { return string(e) }

// Globals are shared by every command.
type Globals struct {
	Endpoint string `help:"Artifact server base URL." env:"ARTIFACT_ENDPOINT" placeholder:"URL"`
	Token    string `help:"Bearer token for the server." env:"ARTIFACT_TOKEN" placeholder:"TOKEN"`
	JSON     bool   `help:"Emit the raw API JSON instead of human output."`
	Quiet    bool   `help:"Suppress all stderr diagnostics." short:"q"`
	// Deliberately not type:"path": that mapper rewrites an empty value to
	// the working directory, which would turn "unset" into "read the cwd".
	Config string `help:"Config file path (default ~/.config/artifact/config.toml)." env:"ARTIFACT_CONFIG" placeholder:"PATH"`
}

// CLI is the command tree. Bare `artifact <file>` falls through to up, because
// the most common action should not need a subcommand.
type CLI struct {
	Globals

	Up      UpCmd      `cmd:"" default:"withargs" help:"Upload a file and print its URL."`
	Update  UpdateCmd  `cmd:"" help:"Upload new contents for an existing artifact."`
	List    ListCmd    `cmd:"" help:"List recent artifacts, newest first."`
	Info    InfoCmd    `cmd:"" help:"Show metadata and version history for an artifact."`
	Rm      RmCmd      `cmd:"" help:"Delete an artifact, all versions, and all blobs."`
	Open    OpenCmd    `cmd:"" help:"Open an artifact in the default browser."`
	Version VersionCmd `cmd:"" help:"Print version, commit, and build date."`
}

func main() {
	os.Exit(run(context.Background(), os.Args[1:], os.Stdout, os.Stderr))
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	var (
		cli        CLI
		parserExit *int
	)

	parser, err := kong.New(&cli,
		kong.Name("artifact"),
		kong.Description("Publish a file and get a URL back."),
		kong.Writers(stdout, stderr),
		kong.Exit(func(code int) { parserExit = &code }),
	)
	if err != nil {
		fmt.Fprintf(stderr, "artifact: %v\n", err)

		return exitError
	}

	kctx, err := parser.Parse(args)

	// --help and similar terminate through kong.Exit rather than returning.
	if parserExit != nil {
		return *parserExit
	}

	if err != nil {
		fmt.Fprintf(stderr, "artifact: %v\nrun 'artifact --help' for usage\n", err)

		return exitUsage
	}

	rt := &cmdContext{ctx: ctx, globals: &cli.Globals, stdout: stdout, stderr: stderr}

	if err := kctx.Run(rt); err != nil {
		rt.warn("artifact: %v", err)

		return exitCodeFor(err)
	}

	return exitOK
}

func exitCodeFor(err error) int {
	var usage usageError
	if errors.As(err, &usage) {
		return exitUsage
	}

	var apiErr *client.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.Status {
		case http.StatusUnauthorized:
			return exitAuth
		case http.StatusNotFound:
			return exitNotFound
		}
	}

	return exitError
}

// cmdContext is what kong passes to every command's Run method.
type cmdContext struct {
	ctx     context.Context
	globals *Globals
	stdout  io.Writer
	stderr  io.Writer

	client *client.Client
}

// api resolves configuration on first use, so `artifact version` works with no
// endpoint or token at all.
func (c *cmdContext) api() (*client.Client, error) {
	if c.client != nil {
		return c.client, nil
	}

	set, err := c.globals.resolve()
	if err != nil {
		return nil, err
	}

	for _, warning := range set.warnings {
		c.warn("%s", warning)
	}

	c.client = client.New(set.endpoint, set.token)

	return c.client, nil
}

// warn writes a diagnostic to stderr. --quiet silences every one of them,
// including errors; the exit code is what a script should read instead.
func (c *cmdContext) warn(format string, args ...any) {
	if c.globals.Quiet {
		return
	}

	fmt.Fprintf(c.stderr, format+"\n", args...)
}

// emitURL is the output contract for the commands that publish: the URL alone
// on stdout, or the server's response verbatim under --json.
func (c *cmdContext) emitURL(art *client.Artifact, raw []byte) error {
	if c.globals.JSON {
		return c.emitJSON(raw)
	}

	_, err := fmt.Fprintln(c.stdout, art.URL)

	return err
}

func (c *cmdContext) emitJSON(raw []byte) error {
	_, err := c.stdout.Write(append(bytes.TrimRight(raw, "\n"), '\n'))

	return err
}

func (c *cmdContext) table() *tabwriter.Writer {
	return tabwriter.NewWriter(c.stdout, 0, 0, 2, ' ', 0)
}

// UpCmd publishes a new artifact.
type UpCmd struct {
	File     string `arg:"" type:"path" help:"File to publish, or - for standard input."`
	Slug     string `help:"Publish at a stable path instead of a random ID." placeholder:"SLUG"`
	Filename string `help:"Override the filename recorded for the upload." placeholder:"NAME"`
}

func (u *UpCmd) Run(c *cmdContext) error {
	filename, err := filenameFor(u.Filename, u.File)
	if err != nil {
		return err
	}

	api, err := c.api()
	if err != nil {
		return err
	}

	art, raw, err := api.Upload(c.ctx, u.File, filename, u.Slug)
	if err != nil {
		return err
	}

	return c.emitURL(art, raw)
}

// UpdateCmd adds a version to an existing artifact, leaving its URL alone.
type UpdateCmd struct {
	ID       string `arg:"" help:"Artifact ID or slug."`
	File     string `arg:"" type:"path" help:"File to publish as the next version, or - for standard input."`
	Filename string `help:"Override the filename recorded for the upload." placeholder:"NAME"`
}

func (u *UpdateCmd) Run(c *cmdContext) error {
	filename, err := filenameFor(u.Filename, u.File)
	if err != nil {
		return err
	}

	api, err := c.api()
	if err != nil {
		return err
	}

	art, raw, err := api.Update(c.ctx, u.ID, u.File, filename)
	if err != nil {
		return err
	}

	return c.emitURL(art, raw)
}

// ListCmd prints recent artifacts as an aligned table.
type ListCmd struct {
	Limit  int    `default:"50" help:"Maximum number of artifacts to return (server caps at 200)."`
	Cursor string `help:"Continue from the cursor of a previous page." placeholder:"CURSOR"`
}

func (l *ListCmd) Run(c *cmdContext) error {
	api, err := c.api()
	if err != nil {
		return err
	}

	list, raw, err := api.List(c.ctx, l.Limit, l.Cursor)
	if err != nil {
		return err
	}

	if c.globals.JSON {
		return c.emitJSON(raw)
	}

	table := c.table()
	fmt.Fprintln(table, "ID\tVER\tSIZE\tUPDATED\tFILENAME\tURL")

	for _, art := range list.Artifacts {
		fmt.Fprintf(table, "%s\t%d\t%s\t%s\t%s\t%s\n",
			art.ID, art.Version, humanSize(art.Size), art.UpdatedAt, art.Filename, art.URL)
	}

	if err := table.Flush(); err != nil {
		return err
	}

	if list.NextCursor != nil {
		c.warn("more results available: --cursor %s", *list.NextCursor)
	}

	return nil
}

// InfoCmd prints metadata and the full version history.
type InfoCmd struct {
	ID string `arg:"" help:"Artifact ID or slug."`
}

func (i *InfoCmd) Run(c *cmdContext) error {
	api, err := c.api()
	if err != nil {
		return err
	}

	info, raw, err := api.Info(c.ctx, i.ID)
	if err != nil {
		return err
	}

	if c.globals.JSON {
		return c.emitJSON(raw)
	}

	header := c.table()
	fmt.Fprintf(header, "ID\t%s\n", info.ID)
	fmt.Fprintf(header, "Slug\t%s\n", orDash(info.Slug))
	fmt.Fprintf(header, "URL\t%s\n", info.URL)
	fmt.Fprintf(header, "Latest\t%d\n", info.LatestVersion)
	fmt.Fprintf(header, "Created\t%s\n", info.CreatedAt)
	fmt.Fprintf(header, "Updated\t%s\n", info.UpdatedAt)

	if err := header.Flush(); err != nil {
		return err
	}

	fmt.Fprintln(c.stdout)

	versions := c.table()
	fmt.Fprintln(versions, "VER\tSIZE\tCREATED\tFILENAME\tURL")

	for _, v := range info.Versions {
		fmt.Fprintf(versions, "%d\t%s\t%s\t%s\t%s\n",
			v.N, humanSize(v.Size), v.CreatedAt, v.Filename, v.URL)
	}

	return versions.Flush()
}

// RmCmd deletes an artifact. Nothing goes to stdout, because there is no URL.
type RmCmd struct {
	ID string `arg:"" help:"Artifact ID or slug."`
}

func (r *RmCmd) Run(c *cmdContext) error {
	api, err := c.api()
	if err != nil {
		return err
	}

	if err := api.Delete(c.ctx, r.ID); err != nil {
		return err
	}

	c.warn("deleted %s", r.ID)

	return nil
}

// OpenCmd looks the artifact up, then hands its URL to the browser.
type OpenCmd struct {
	ID string `arg:"" help:"Artifact ID or slug."`
}

func (o *OpenCmd) Run(c *cmdContext) error {
	api, err := c.api()
	if err != nil {
		return err
	}

	info, raw, err := api.Info(c.ctx, o.ID)
	if err != nil {
		return err
	}

	if err := openBrowser(info.URL); err != nil {
		return err
	}

	if c.globals.JSON {
		return c.emitJSON(raw)
	}

	_, err = fmt.Fprintln(c.stdout, info.URL)

	return err
}

// VersionCmd needs no server, so it never resolves an endpoint or token.
type VersionCmd struct{}

func (v *VersionCmd) Run(c *cmdContext) error {
	if c.globals.JSON {
		return json.NewEncoder(c.stdout).Encode(map[string]string{
			"version": Version,
			"commit":  Commit,
			"date":    Date,
		})
	}

	_, err := fmt.Fprintf(c.stdout, "artifact %s (%s) built %s\n", Version, Commit, Date)

	return err
}

func openBrowser(target string) error {
	var cmd *exec.Cmd

	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", target)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", target)
	default:
		cmd = exec.Command("xdg-open", target)
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("open browser: %w", err)
	}

	return nil
}

// filenameFor picks the recorded filename, whose extension decides how the
// URL renders. Standard input has no name, so it must be given one.
func filenameFor(override, path string) (string, error) {
	if override != "" {
		return override, nil
	}

	if path == client.Stdin {
		return "", usageError("publishing standard input needs --filename, whose extension decides how the URL renders")
	}

	return filepath.Base(path), nil
}

func orDash(s *string) string {
	if s == nil || *s == "" {
		return "-"
	}

	return *s
}

func humanSize(n int64) string {
	const unit = 1024

	if n < unit {
		return fmt.Sprintf("%d B", n)
	}

	div, exp := int64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}

	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
