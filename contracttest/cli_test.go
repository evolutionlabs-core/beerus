package contracttest

import (
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/creack/pty"

	"github.com/evolutionlabs-core/beerus/internal/config"
)

type cli struct {
	t    *testing.T
	bin  string
	dir  string
	home string
	in   string
}

func newCLI(t *testing.T, s *Stack) *cli {
	t.Helper()
	return newCLIAt(t, s.cfg.Dir)
}

func newCLIAt(t *testing.T, dir string) *cli {
	t.Helper()
	return &cli{t: t, bin: builtBinary(t), dir: dir, home: t.TempDir()}
}

func (c *cli) env(extra ...string) []string {
	env := []string{"PATH=" + filepath.Dir(c.bin) + ":" + os.Getenv("PATH"), "HOME=" + c.home, "TMPDIR=" + os.TempDir(), "BEERUS_DIR=" + c.dir, "TERM=xterm", "LANG=en_US.UTF-8", "BASH_SILENCE_DEPRECATION_WARNING=1"}
	for _, key := range []string{"PGHOST", "PGPORT", "PGUSER", "PGPASSWORD"} {
		if v := os.Getenv(key); v != "" {
			env = append(env, key+"="+v)
		}
	}
	if v := cmp.Or(os.Getenv(config.DatabaseURLEnv), os.Getenv("BEERUS_TEST_DATABASE_URL")); v != "" {
		env = append(env, config.DatabaseURLEnv+"="+v)
	}
	return append(env, extra...)
}

func (c *cli) run(args ...string) (string, string, int) {
	c.t.Helper()
	cmd := exec.Command(c.bin, args...)
	cmd.Env = c.env()
	cmd.Stdin = strings.NewReader(c.in)
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	err := cmd.Run()
	code := 0
	var exit *exec.ExitError
	switch {
	case errors.As(err, &exit):
		code = exit.ExitCode()
	case err != nil:
		c.t.Fatalf("run beerus %v: %v", args, err)
	}
	return out.String(), strings.TrimSpace(errOut.String()), code
}

func (c *cli) ok(args ...string) string {
	c.t.Helper()
	out, errOut, code := c.run(args...)
	if code != 0 {
		c.t.Fatalf("beerus %v exited %d: %s", args, code, errOut)
	}
	return out
}

func (c *cli) fails(args ...string) string {
	c.t.Helper()
	out, errOut, code := c.run(args...)
	if code != 1 || out != "" {
		c.t.Fatalf("beerus %v: want exit 1 and no output, got exit %d, output %q, error %q", args, code, out, errOut)
	}
	return errOut
}

func (c *cli) json(args ...string) map[string]any {
	c.t.Helper()
	return c.printedJSON(append(args, "--json")...)
}

func (c *cli) printedJSON(args ...string) map[string]any {
	c.t.Helper()
	out := map[string]any{}
	if err := json.Unmarshal([]byte(c.ok(args...)), &out); err != nil {
		c.t.Fatalf("beerus %v is not JSON: %v", args, err)
	}
	return out
}

func (c *cli) capture(pattern string, args ...string) (string, string) {
	c.t.Helper()
	out := c.ok(args...)
	m := regexp.MustCompile(pattern).FindStringSubmatch(out)
	if m == nil {
		c.t.Fatalf("beerus %v: no match for %s in %q", args, pattern, out)
	}
	return m[1], out
}

type console struct {
	mu      sync.Mutex
	buf     bytes.Buffer
	changed chan struct{}
	closed  bool
}

var ansi = regexp.MustCompile("\x1b\\[[0-9;?]*[a-zA-Z]|\x1b\\][^\a]*\a|\x1b[=>]|\r")

func startConsole(t *testing.T, c *cli, shell string, extra []string, args ...string) (*console, *os.File, *exec.Cmd) {
	t.Helper()
	cmd := exec.Command(c.bin, args...)
	cmd.Env = c.env(append([]string{"SHELL=" + shell}, extra...)...)
	tty, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 40, Cols: 200})
	if err != nil {
		t.Fatalf("start beerus %v in a terminal: %v", args, err)
	}
	con := &console{changed: make(chan struct{}, 1)}
	go func() {
		chunk := make([]byte, 4096)
		for {
			n, err := tty.Read(chunk)
			con.mu.Lock()
			con.buf.Write(chunk[:n])
			con.closed = err != nil
			con.mu.Unlock()
			select {
			case con.changed <- struct{}{}:
			default:
			}
			if err != nil {
				return
			}
		}
	}()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = tty.Close()
	})
	return con, tty, cmd
}

func (c *console) text() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return ansi.ReplaceAllString(c.buf.String(), "")
}

func (c *console) waitFor(t *testing.T, from int, pattern string) string {
	t.Helper()
	re := regexp.MustCompile(pattern)
	deadline := time.After(30 * time.Second)
	for {
		all := c.text()
		if from <= len(all) {
			if m := re.FindStringSubmatch(all[from:]); m != nil {
				return m[len(m)-1]
			}
		}
		select {
		case <-c.changed:
		case <-time.After(50 * time.Millisecond):
		case <-deadline:
			t.Fatalf("waited 30s for %q in the terminal output:\n%s", pattern, all)
		}
	}
}

func (c *console) send(t *testing.T, tty *os.File, s string) int {
	t.Helper()
	from := len(c.text())
	if _, err := tty.WriteString(s); err != nil {
		t.Fatalf("type %q: %v", s, err)
	}
	return from
}

func watTime() string {
	return `[A-Z][a-z]{2} \d{1,2} [A-Z][a-z]{2} \d{4} \d\d:\d\d WAT`
}

func matches(pattern, got string) bool {
	return regexp.MustCompile("^" + pattern + "$").MatchString(got)
}
