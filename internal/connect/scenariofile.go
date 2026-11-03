package connect

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/evolutionlabs-core/beerus/internal/world"
)

const maxScenarioFile = 1 << 20

type scenarioFile struct {
	Name        string         `yaml:"name"`
	Description string         `yaml:"description"`
	Steps       []scenarioStep `yaml:"steps"`
}

type scenarioStep struct {
	Run         string        `yaml:"run"`
	Account     string        `yaml:"account"`
	Institution string        `yaml:"institution"`
	Persona     string        `yaml:"persona"`
	Connection  string        `yaml:"connection"`
	Duration    string        `yaml:"duration"`
	Count       int           `yaml:"count"`
	Transfer    *transferBody `yaml:"transfer"`
	Advance     string        `yaml:"advance"`
	Suspend     string        `yaml:"suspend"`
	Resume      string        `yaml:"resume"`
}

var unknownFieldRe = regexp.MustCompile(`line (\d+): field (\S+) not found`)

func parseScenarioFile(raw []byte) (*scenarioFile, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, fmt.Errorf("the scenario file is empty")
	}
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	var f scenarioFile
	if err := dec.Decode(&f); err != nil {
		if m := unknownFieldRe.FindStringSubmatch(err.Error()); m != nil {
			return nil, fmt.Errorf("line %s: unknown field %q", m[1], m[2])
		}
		msg, _, _ := strings.Cut(strings.TrimPrefix(err.Error(), "yaml: "), "\n")
		return nil, fmt.Errorf("the file is not valid YAML: %s", msg)
	}
	return &f, nil
}

func (s scenarioStep) action() (string, int) {
	var names []string
	if s.Run != "" {
		names = append(names, "run")
	}
	if s.Transfer != nil {
		names = append(names, "transfer")
	}
	if s.Advance != "" {
		names = append(names, "advance")
	}
	if s.Suspend != "" {
		names = append(names, "suspend")
	}
	if s.Resume != "" {
		names = append(names, "resume")
	}
	if len(names) == 0 {
		return "", 0
	}
	return names[0], len(names)
}

func (a *App) validateScenarioFile(f *scenarioFile) error {
	if strings.TrimSpace(f.Name) == "" {
		return fmt.Errorf("name is required")
	}
	if len(f.Steps) == 0 {
		return fmt.Errorf("steps is empty; add at least one step")
	}
	known := []string{"token_refresh_failed"}
	for _, sc := range world.Scenarios {
		known = append(known, sc.Name)
	}
	for i, s := range f.Steps {
		bad := func(format string, args ...any) error {
			return fmt.Errorf("step %d: %s", i+1, fmt.Sprintf(format, args...))
		}
		action, n := s.action()
		switch {
		case n == 0:
			return bad("use one of run, transfer, advance, suspend or resume")
		case n > 1:
			return bad("use only one of run, transfer, advance, suspend or resume")
		}
		if action != "run" && (s.Account != "" || s.Institution != "" || s.Persona != "" || s.Connection != "" || s.Duration != "" || s.Count != 0) {
			return bad("account, institution, persona, connection, duration and count only go with run")
		}
		switch action {
		case "run":
			if !slices.Contains(known, s.Run) {
				return bad("unknown scenario %q; run beerus scenarios to list them", s.Run)
			}
			if s.Duration != "" {
				if _, err := parseDuration(s.Duration); err != nil {
					return bad("duration must look like 2h or 3d")
				}
			}
			if s.Persona != "" {
				if _, ok := a.world.Persona(s.Persona); !ok {
					return bad("unknown persona %q", s.Persona)
				}
			}
			if s.Institution != "" {
				if _, ok := a.world.Institution(s.Institution); !ok {
					return bad("unknown bank %q", s.Institution)
				}
			}
		case "transfer":
			t := s.Transfer
			if t.From == "" || t.To == "" || t.Amount == "" {
				return bad("transfer needs from, to and amount")
			}
			if _, err := parseNaira(t.Amount); err != nil {
				return bad("%s", err.Error())
			}
		case "advance":
			if _, err := parseDuration(s.Advance); err != nil {
				return bad("advance must look like 24h or 3d")
			}
		case "suspend", "resume":
			id := s.Suspend + s.Resume
			if _, ok := a.world.Institution(id); !ok {
				return bad("unknown bank %q", id)
			}
		}
	}
	return nil
}

func (a *App) resolveRef(ref string) (string, error) {
	who, where, ok := strings.Cut(ref, "@")
	if !ok {
		return ref, nil
	}
	bank, typ, _ := strings.Cut(where, ":")
	if _, ok := a.world.Persona(who); !ok {
		return "", fmt.Errorf("unknown persona %q", who)
	}
	for _, acc := range a.world.AccountsAt(who, bank) {
		if typ == "" || acc.Type == typ {
			return acc.Key, nil
		}
	}
	return "", fmt.Errorf("%s has no matching account at %s", who, bank)
}

func bankMessage(err error) string {
	if errors.Is(err, errNotFound) {
		return "no such bank"
	}
	return strings.TrimPrefix(err.Error(), ErrBankInvalid.Error()+": ")
}

func (a *App) runScenarioStep(s scenarioStep) (string, string, error) {
	action, _ := s.action()
	switch action {
	case "run":
		account := s.Account
		if account != "" {
			var err error
			if account, err = a.resolveRef(account); err != nil {
				return s.Run, "", err
			}
		}
		result, err := a.runScenario(s.Run, scenarioParams{AccountID: account, Institution: s.Institution, Persona: s.Persona, ConnectionID: s.Connection, Duration: s.Duration, Count: s.Count})
		if err != nil {
			return s.Run, "", err
		}
		return s.Run, result, nil
	case "transfer":
		t := *s.Transfer
		var err error
		if t.From, err = a.resolveRef(t.From); err != nil {
			return "transfer", "", err
		}
		if t.To, err = a.resolveRef(t.To); err != nil {
			return "transfer", "", err
		}
		res, err := a.runTransfer(t)
		if err != nil {
			return "transfer", "", errors.New(transferMessage(err))
		}
		return "transfer", fmt.Sprintf("%s moved, %s in charges", naira(res.Amount), naira(res.Fee+res.VAT+res.StampDuty)), nil
	case "advance":
		if err := a.advanceClock(s.Advance, ""); err != nil {
			return "advance " + s.Advance, "", err
		}
		return "advance " + s.Advance, "clock at " + ts(a.clock.Now()), nil
	case "suspend":
		if err := a.SetBankSuspended(s.Suspend, true); err != nil {
			return "suspend " + s.Suspend, "", errors.New(bankMessage(err))
		}
		return "suspend " + s.Suspend, s.Suspend + " is suspended", nil
	default:
		if err := a.SetBankSuspended(s.Resume, false); err != nil {
			return "resume " + s.Resume, "", errors.New(bankMessage(err))
		}
		return "resume " + s.Resume, s.Resume + " is back", nil
	}
}

func (a *App) scenarioFilePath(p string) (string, error) {
	if a.cfg.Dir == "" {
		return "", fmt.Errorf("there is no .beerus folder to read from")
	}
	p = filepath.ToSlash(filepath.Clean(p))
	p = strings.TrimPrefix(strings.TrimPrefix(p, ".beerus/"), "scenarios/")
	if p == "" || p == "." || strings.HasPrefix(p, "/") || strings.HasPrefix(p, "..") || strings.Contains(p, "/../") {
		return "", fmt.Errorf("path must be a file under .beerus/scenarios/")
	}
	if ext := filepath.Ext(p); ext != ".yaml" && ext != ".yml" {
		return "", fmt.Errorf("scenario files end in .yaml or .yml")
	}
	return a.cfg.Path("scenarios", filepath.FromSlash(p)), nil
}

func (a *App) ctlScenarioRun(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxScenarioFile+1))
	if err != nil || len(raw) > maxScenarioFile {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "The scenario file is too large.")
		return
	}
	path := r.URL.Query().Get("path")
	if strings.Contains(r.Header.Get("Content-Type"), "json") {
		var body struct {
			Path string `json:"path"`
		}
		if json.Unmarshal(raw, &body) == nil && body.Path != "" {
			path = body.Path
		}
	}
	if path != "" {
		full, err := a.scenarioFilePath(path)
		if err != nil {
			writeError(w, r, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		if raw, err = os.ReadFile(full); err != nil {
			writeError(w, r, http.StatusNotFound, "not_found", "No such file under .beerus/scenarios/.")
			return
		}
	}
	f, err := parseScenarioFile(raw)
	if err == nil {
		err = a.validateScenarioFile(f)
	}
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	steps := []map[string]any{}
	for i, s := range f.Steps {
		label, result, err := a.runScenarioStep(s)
		if err != nil {
			msg := fmt.Sprintf("step %d (%s) failed: %s", i+1, label, err)
			if i > 0 {
				msg += fmt.Sprintf(" Steps 1 to %d already ran.", i)
			}
			writeError(w, r, http.StatusBadRequest, "invalid_request", msg)
			return
		}
		steps = append(steps, map[string]any{"step": i + 1, "action": label, "result": result})
	}
	writeJSON(w, http.StatusOK, map[string]any{"scenario": f.Name, "steps": steps})
}
