package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"

	"github.com/evolutionlabs-core/beerus/internal/connect"
)

func cmdReset(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("reset", flag.ContinueOnError)
	dir := fs.String("dir", defaultDir(), "state directory")
	asJSON := fs.Bool("json", false, "print the dropped databases as JSON")
	var name string
	if len(args) > 0 && args[0] != "" && args[0][0] != '-' {
		name, args = args[0], args[1:]
	}
	if err := parseFlags("reset", fs, args); err != nil {
		return err
	}
	if name == "" && fs.NArg() > 0 {
		name = fs.Arg(0)
	}
	if name == "" {
		return usageError("reset")
	}
	t, err := connect.FindResetTarget(*dir)
	if err != nil {
		return err
	}
	if err := t.Check(name); err != nil {
		return err
	}
	dropped, err := connect.Reset(context.Background(), t.Dir, t.DatabaseURL)
	if err != nil {
		return err
	}
	if *asJSON {
		raw, err := json.MarshalIndent(map[string]any{"dir": t.Dir, "dropped": append([]string{}, dropped...)}, "", "  ")
		if err != nil {
			return fmt.Errorf("encode reset result: %w", err)
		}
		_, err = fmt.Fprintln(out, string(raw))
		return err
	}
	if len(dropped) == 0 {
		fmt.Fprintln(out, "No databases to drop.")
	} else {
		fmt.Fprintln(out, "Dropped databases:")
		for _, d := range dropped {
			fmt.Fprintln(out, "  "+d)
		}
	}
	fmt.Fprintf(out, "Cleared %s (kept %s). Run beerus up to start again.\n", t.Dir, connect.LocalFile)
	return nil
}
