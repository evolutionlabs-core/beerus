package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"

	"github.com/evolutionlabs-core/beerus/internal/config"
)

const trustName = "Beerus CBN Trust Anchor"

func cmdTrust(args []string, add bool) error {
	fs := flag.NewFlagSet("trust", flag.ExitOnError)
	dir := fs.String("dir", defaultDir(), "state directory")
	fs.Parse(args)
	c, err := config.Load(*dir)
	if err != nil {
		return err
	}
	root := c.Path("ca", "root.crt")
	if _, err := os.Stat(root); err != nil {
		return err
	}
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		keychain := filepath.Join(home, "Library", "Keychains", "login.keychain-db")
		if add {
			cmd = exec.Command("security", "add-trusted-cert", "-r", "trustRoot", "-k", keychain, root)
		} else {
			cmd = exec.Command("security", "delete-certificate", "-c", trustName, keychain)
		}
	case "linux":
		target := "/usr/local/share/ca-certificates/beerus-root.crt"
		if add {
			cmd = exec.Command("sudo", "sh", "-c", fmt.Sprintf("cp %q %q && update-ca-certificates", root, target))
		} else {
			cmd = exec.Command("sudo", "sh", "-c", fmt.Sprintf("rm -f %q && update-ca-certificates --fresh", target))
		}
	default:
		return errors.New("beerus trust supports macOS and Linux; add " + root + " to your trust store by hand")
	}
	if add {
		fmt.Printf("Adding %s to the system trust store.\nIt is name-constrained to localhost and *.localhost, so it cannot vouch for real sites.\nYour system may ask for your password.\n", root)
	} else {
		fmt.Println("Removing the Beerus root certificate from the system trust store.")
	}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return err
	}
	if add {
		fmt.Println("Done. Restart your browser. Firefox keeps its own store: import the certificate there if you use it.")
	} else {
		fmt.Println("Done.")
	}
	return nil
}
