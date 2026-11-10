package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/connect"
)

const (
	sessionEnv    = "BEERUS_SESSION"
	noBankMessage = "No bank selected. Run beerus use <bank> or add --bank."
)

const zshEnv = `[[ -r "${BEERUS_USER_ZDOTDIR:-$HOME}/.zshenv" ]] && source "${BEERUS_USER_ZDOTDIR:-$HOME}/.zshenv"
`

const zshRC = `_beerus_home="${BEERUS_USER_ZDOTDIR:-$HOME}"
if [[ -n "$BEERUS_USER_ZDOTDIR" ]]; then export ZDOTDIR="$BEERUS_USER_ZDOTDIR"; else unset ZDOTDIR; fi
unset BEERUS_USER_ZDOTDIR
[[ -r "$_beerus_home/.zshrc" ]] && source "$_beerus_home/.zshrc"
unset _beerus_home
_beerus_busy=0
_beerus_prompt() {
  local b
  _beerus_busy=0
  { b=$(<"$BEERUS_SESSION"); } 2>/dev/null
  PROMPT="%~ (${b//\%/%%}) $ "
}
_beerus_preexec() { _beerus_busy=1 }
precmd_functions+=(_beerus_prompt)
preexec_functions+=(_beerus_preexec)
TRAPINT() {
  if (( ! _beerus_busy )) && [[ -z "$BUFFER" ]]; then exit; fi
  return $(( 128 + $1 ))
}
`

const bashRC = `[[ -r "$HOME/.bashrc" ]] && source "$HOME/.bashrc"
_beerus_intr=$(stty -a 2>/dev/null | sed -n 's/.*intr = \([^;]*\);.*/\1/p')
_beerus_quiet=0
_beerus_prompt() {
  local b
  { b=$(<"$BEERUS_SESSION"); } 2>/dev/null
  PS1="\w (${b}) \$ "
  _beerus_quiet=1
  stty intr undef 2>/dev/null
}
_beerus_running() {
  if [[ $_beerus_quiet == 1 ]]; then _beerus_quiet=0; stty intr "$_beerus_intr" 2>/dev/null; fi
}
_beerus_interrupt() {
  if [[ -z "$READLINE_LINE" ]]; then exit; fi
  READLINE_LINE=""
  READLINE_POINT=0
}
PROMPT_COMMAND="${PROMPT_COMMAND:+$PROMPT_COMMAND;}_beerus_prompt"
trap _beerus_running DEBUG
trap 'stty intr "$_beerus_intr" 2>/dev/null' EXIT
bind -x '"\C-c": _beerus_interrupt'
`

func sessionBank() (string, error) {
	path := os.Getenv(sessionEnv)
	if path == "" {
		return "", nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read the session file %s: %w. Run beerus use <bank> again", path, err)
	}
	id := strings.TrimSpace(string(raw))
	if id == "" {
		return "", fmt.Errorf("the session file %s is empty. Run beerus use <bank> again", path)
	}
	return id, nil
}

func resolveBank(flagValue string) (string, error) {
	if flagValue != "" {
		return flagValue, nil
	}
	id, err := sessionBank()
	if err != nil {
		return "", err
	}
	if id == "" {
		return "", errors.New(noBankMessage)
	}
	return id, nil
}

func cmdUse(args []string) error {
	if len(args) != 1 || strings.HasPrefix(args[0], "-") {
		return errors.New("usage: beerus use <bank>")
	}
	id := args[0]
	c, err := newCtl()
	if err != nil {
		return err
	}
	st, err := bankStatus(c, id)
	if err != nil {
		return err
	}
	banner, err := sessionBanner(c, st)
	if err != nil {
		return err
	}
	if path := os.Getenv(sessionEnv); path != "" {
		if _, serr := os.Stat(path); serr == nil {
			if err := config.WriteSecret(path, id+"\n"); err != nil {
				return fmt.Errorf("switch the session to %s: %w", id, err)
			}
			fmt.Println(strings.Replace(banner, "Using ", "Switched to ", 1))
			return nil
		}
	}
	return runSession(id, banner)
}

func bankStatus(c *ctl, id string) (connect.BankStatus, error) {
	out, err := c.do("GET", "/banks", nil)
	if err != nil {
		return connect.BankStatus{}, err
	}
	banks, err := decodeList[connect.BankStatus](out)
	if err != nil {
		return connect.BankStatus{}, err
	}
	ids := make([]string, 0, len(banks))
	for _, b := range banks {
		if b.ID == id {
			return b, nil
		}
		ids = append(ids, b.ID)
	}
	return connect.BankStatus{}, fmt.Errorf("bank %q is not in this setup; the banks are %s", id, strings.Join(ids, ", "))
}

func sessionBanner(c *ctl, st connect.BankStatus) (string, error) {
	now, err := businessNow(c)
	if err != nil {
		return "", err
	}
	out, err := c.do("GET", "/faults", nil)
	if err != nil {
		return "", err
	}
	faults, err := decodeList[connect.Fault](out)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Using %s. Business time: %s. Type exit to leave.", st.Name, stamp(now))
	for _, f := range faults {
		if f.Bank == st.ID {
			fmt.Fprintf(&b, "\nFault: %s", describeFault(f))
		}
	}
	return b.String(), nil
}

func runSession(id, banner string) error {
	shell := os.Getenv("SHELL")
	if shell == "" {
		return errors.New("$SHELL is not set. Set it to your shell, such as /bin/zsh")
	}
	dir, err := os.MkdirTemp("", "beerus-session-")
	if err != nil {
		return fmt.Errorf("create the session folder: %w", err)
	}
	defer os.RemoveAll(dir)
	file := filepath.Join(dir, "session")
	if err := config.WriteSecret(file, id+"\n"); err != nil {
		return fmt.Errorf("write the session file: %w", err)
	}
	env := append(os.Environ(), sessionEnv+"="+file)
	var args []string
	switch filepath.Base(shell) {
	case "zsh":
		for name, body := range map[string]string{".zshenv": zshEnv, ".zshrc": zshRC} {
			if err := config.WriteSecret(filepath.Join(dir, name), body); err != nil {
				return fmt.Errorf("write the temporary %s: %w", name, err)
			}
		}
		env = append(env, "BEERUS_USER_ZDOTDIR="+os.Getenv("ZDOTDIR"), "ZDOTDIR="+dir)
	case "bash":
		rc := filepath.Join(dir, "bashrc")
		if err := config.WriteSecret(rc, bashRC); err != nil {
			return fmt.Errorf("write the temporary bashrc: %w", err)
		}
		args = []string{"--rcfile", rc, "-i"}
	}
	cmd := exec.Command(shell, args...)
	cmd.Env = env
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	sig := make(chan os.Signal, 4)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer signal.Stop(sig)
	fmt.Println(banner)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start %s: %w", shell, err)
	}
	done := make(chan struct{})
	go func() {
		for {
			select {
			case s := <-sig:
				if s != os.Interrupt {
					_ = cmd.Process.Signal(s)
				}
			case <-done:
				return
			}
		}
	}()
	err = cmd.Wait()
	close(done)
	var exit *exec.ExitError
	if err != nil && !errors.As(err, &exit) {
		return fmt.Errorf("wait for %s: %w", shell, err)
	}
	return nil
}
