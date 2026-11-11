package connect

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/setup"
)

const (
	SpecFile    = "setup.yaml"
	LocalFile   = "local.yaml"
	oldSpecFile = "beerus.yaml"
	statusFile  = "control.json"

	defaultDatabaseURL  = "postgres:///postgres"
	defaultConnectHost  = "connect.localhost"
	defaultPartnerPort  = 7443
	defaultWidgetPort   = 7444
	defaultControlPort  = 7480
	defaultHealthPort   = 7490
	defaultSyncInterval = "1m"
	defaultRefreshEvery = "1m"

	defaultLegalName  = "Beerus Connect Limited"
	defaultBrandName  = "Beerus Connect"
	defaultRegistryID = "OBR-T2-000001"
	defaultCACNumber  = "RC 0000001"
	defaultPurpose    = "to read your account details, balances and transactions"

	stepDone = "done"
)

var ErrFutureWorld = errors.New("This setup was moved ahead of real time by an older Beerus. Run beerus reset, then beerus up.")

var ErrOldSetup = errors.New("This setup was made by an older Beerus. Run beerus reset, then beerus up.")

func refuseOld(dir string) error {
	if _, err := os.Stat(filepath.Join(dir, oldSpecFile)); err != nil {
		return nil
	}
	if _, err := os.Stat(filepath.Join(dir, statusFile)); errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("%s: %w", dir, ErrOldSetup)
	}
	return nil
}

func Initialized(dir string) (bool, error) {
	if err := refuseOld(dir); err != nil {
		return false, err
	}
	_, err := os.Stat(filepath.Join(dir, statusFile))
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, os.ErrNotExist):
		return false, nil
	default:
		return false, fmt.Errorf("look for %s: %w", statusFile, err)
	}
}

func DefaultLocal() setup.Local {
	return setup.Local{
		DatabaseURL: defaultDatabaseURL,
		Connect: setup.LocalConnect{
			Host:         defaultConnectHost,
			PartnerPort:  defaultPartnerPort,
			WidgetPort:   defaultWidgetPort,
			ControlPort:  defaultControlPort,
			HealthPort:   defaultHealthPort,
			SyncInterval: defaultSyncInterval,
			RefreshEvery: defaultRefreshEvery,
		},
	}
}

func FreshLocal(dir string) (setup.Local, error) {
	var local setup.Local
	found, err := readYAML(filepath.Join(dir, LocalFile), &local)
	if err != nil {
		return setup.Local{}, err
	}
	if !found {
		return DefaultLocal(), nil
	}
	return local, nil
}

func Init(dir string, spec setup.Spec, local setup.Local, today clock.Date) error {
	ok, err := Initialized(dir)
	if err != nil {
		return err
	}
	if ok {
		return fmt.Errorf("%s already holds a Beerus setup", dir)
	}
	if errs := setup.Validate(spec); len(errs) > 0 {
		return errors.Join(errs...)
	}
	plan := setup.Build(spec, setup.Status{}, nil, today)
	if len(plan.Errors) > 0 {
		return errors.Join(plan.Errors...)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	if err := writeYAML(filepath.Join(dir, LocalFile), local); err != nil {
		return err
	}
	if local, err = setup.LoadLocal(dir); err != nil {
		return err
	}
	key, err := newSetupKey()
	if err != nil {
		return err
	}
	st := setup.Status{Version: setup.StatusVersion, Setup: spec.Metadata.Name, SetupKey: key, Applied: pinCodes(spec, plan)}
	st.Resources = []setup.Resource{
		{Kind: setup.KindSystem, ID: setup.SystemCBN, Phase: setup.PhaseActive},
		{Kind: setup.KindSystem, ID: setup.SystemNIBSS, Phase: setup.PhaseProvisioning},
	}
	cfg, err := buildConfig(dir, st, local)
	if err != nil {
		return err
	}
	if err := allocateBanks(cfg, &st); err != nil {
		return err
	}
	keys, err := issuePartnerKeys(dir, st.Applied.Spec.Partners)
	if err != nil {
		return err
	}
	st.Resources = append(st.Resources, keys...)
	if cfg, err = buildConfig(dir, st, local); err != nil {
		return err
	}
	if err := config.Prepare(cfg, time.Now()); err != nil {
		return err
	}
	recordCertPrints(cfg, &st)
	if st.AppliedSHA256, err = specSum(st.Applied); err != nil {
		return err
	}
	raw, err := setup.Export(spec)
	if err != nil {
		return err
	}
	if err := config.WriteSecret(filepath.Join(dir, SpecFile), string(raw)); err != nil {
		return fmt.Errorf("write %s: %w", SpecFile, err)
	}
	if _, err := createStore(dir, st); err != nil {
		return err
	}
	return nil
}

func writeYAML(path string, v any) error {
	raw, err := yaml.Marshal(v)
	if err != nil {
		return fmt.Errorf("encode %s: %w", filepath.Base(path), err)
	}
	if err := config.WriteSecret(path, string(raw)); err != nil {
		return fmt.Errorf("write %s: %w", filepath.Base(path), err)
	}
	return nil
}

func specSum(s setup.Spec) (string, error) {
	raw, err := setup.Export(s)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func pinCodes(spec setup.Spec, plan setup.Plan) setup.Spec {
	out := spec
	out.Spec.Banks = append([]setup.Bank(nil), spec.Spec.Banks...)
	out.Spec.Partners = append([]setup.Partner(nil), spec.Spec.Partners...)
	codes := map[string]map[string]string{}
	for _, a := range plan.Actions {
		if a.Op != setup.OpCreate || a.Kind != setup.KindBank {
			continue
		}
		codes[a.ID] = map[string]string{}
		for _, f := range a.Fields {
			codes[a.ID][f.Path] = f.To
		}
	}
	for i := range out.Spec.Banks {
		b := &out.Spec.Banks[i]
		b.NIPCode, b.NUBANCode = codes[b.ID]["nip_code"], codes[b.ID]["nuban_code"]
	}
	return out
}

func anyPort(int) bool { return true }

func allocateBanks(cfg *config.Config, st *setup.Status) error {
	used := map[int]bool{cfg.Connect.PartnerPort: true, cfg.Connect.WidgetPort: true, cfg.Connect.ControlPort: true, cfg.Connect.HealthPort: true}
	next := cfg.Connect.PartnerPort
	for _, b := range st.Applied.Spec.Banks {
		port, err := cfg.AllocatePort(next, used, anyPort)
		if err != nil {
			return fmt.Errorf("%s: online banking: %w", b.ID, err)
		}
		used[port] = true
		api, err := cfg.AllocatePort(port+cfg.APIPortOffset(), used, anyPort)
		if err != nil {
			return fmt.Errorf("%s: bank API: %w", b.ID, err)
		}
		used[api] = true
		next = port + 1
		r := setup.Resource{
			Kind:       setup.KindBank,
			ID:         b.ID,
			Generation: 1,
			Phase:      setup.PhaseProvisioning,
			Allocated:  setup.Allocation{NIPCode: b.NIPCode, NUBANCode: b.NUBANCode, Database: config.DatabaseName(cfg.Dir, b.ID), Port: port, APIPort: api},
			Steps:      map[string]string{setup.StepAllocate: stepDone},
		}
		r.Owned = []setup.Owned{{Kind: ownedPort, Name: fmt.Sprint(port)}, {Kind: ownedPort, Name: fmt.Sprint(api)}}
		st.Resources = append(st.Resources, r)
	}
	return nil
}

func issuePartnerKeys(dir string, partners []setup.Partner) ([]setup.Resource, error) {
	var out []setup.Resource
	for _, p := range partners {
		key, err := writeAPIKey(dir, p.ID)
		if err != nil {
			return nil, err
		}
		out = append(out, setup.Resource{
			Kind:       setup.KindPartner,
			ID:         p.ID,
			Generation: 1,
			Phase:      setup.PhaseActive,
			Partner:    &setup.PartnerKeys{CertCN: p.ID, APIKeySHA256: config.HashKey(key)},
			Owned:      []setup.Owned{{Kind: ownedDir, Name: filepath.Join("partners", p.ID)}},
		})
	}
	return out, nil
}

func writeAPIKey(dir, id string) (string, error) {
	key, err := config.NewAPIKey()
	if err != nil {
		return "", fmt.Errorf("make API key for %s: %w", id, err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "partners", id), 0o700); err != nil {
		return "", fmt.Errorf("create partners/%s: %w", id, err)
	}
	if err := config.WriteSecret(filepath.Join(dir, "partners", id, "api-key"), key+"\n"); err != nil {
		return "", fmt.Errorf("write API key for %s: %w", id, err)
	}
	return key, nil
}

func recordCertPrints(cfg *config.Config, st *setup.Status) {
	for _, p := range cfg.Partners {
		if r := findResource(st, setup.KindPartner, p.ID); r != nil && r.Partner != nil {
			r.Partner.CertSHA256 = p.CertSHA256
		}
	}
}

func LoadConfig(dir string) (*config.Config, error) {
	if err := refuseOld(dir); err != nil {
		return nil, err
	}
	st, err := setup.LoadStatus(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("%s holds no Beerus setup; run beerus up to make one", dir)
		}
		return nil, err
	}
	local, err := setup.LoadLocal(dir)
	if err != nil {
		return nil, err
	}
	return buildConfig(dir, st, local)
}

func buildConfig(dir string, st setup.Status, local setup.Local) (*config.Config, error) {
	spec := st.Applied.Spec
	start, err := time.Parse(time.RFC3339, spec.Start)
	if err != nil {
		return nil, fmt.Errorf("start %q: %w", spec.Start, err)
	}
	def := setup.DefaultSpec(st.Applied.Metadata.Name, clock.DateOf(start)).Spec
	tsqAfter, err := time.ParseDuration(orDefault(spec.NIP.TSQAfter, def.NIP.TSQAfter))
	if err != nil {
		return nil, fmt.Errorf("nip.tsq_after %q: %w", spec.NIP.TSQAfter, err)
	}
	tsqAttempts := spec.NIP.TSQAttempts
	if tsqAttempts == 0 {
		tsqAttempts = def.NIP.TSQAttempts
	}
	ln := local.Connect
	c := &config.Config{
		Dir:         dir,
		DatabaseURL: local.DatabaseURL,
		Start:       start,
		NIPSessions: spec.NIP.Sessions,
		TSQAfter:    tsqAfter,
		TSQAttempts: tsqAttempts,
		Ports:       config.Ports{APIOffset: local.Ports.APIOffset, Window: local.Ports.Window},
		Connect: config.Connect{
			Host:         orDefault(ln.Host, defaultConnectHost),
			PartnerPort:  ln.PartnerPort,
			WidgetPort:   ln.WidgetPort,
			WidgetTLS:    ln.WidgetTLS,
			ControlPort:  ln.ControlPort,
			HealthPort:   ln.HealthPort,
			SyncInterval: ln.SyncInterval,
			RefreshEvery: ln.RefreshEvery,
			PauseLease:   ln.PauseLease,
			LegalName:    orDefault(spec.Connect.LegalName, defaultLegalName),
			BrandName:    orDefault(spec.Connect.BrandName, defaultBrandName),
			RegistryID:   orDefault(spec.Connect.RegistryID, defaultRegistryID),
			CACNumber:    orDefault(spec.Connect.CACNumber, defaultCACNumber),
		},
	}
	if len(spec.Holidays.Add)+len(spec.Holidays.Remove) > 0 {
		c.Holidays = &config.Holidays{Add: spec.Holidays.Add, Remove: spec.Holidays.Remove}
	}
	for _, b := range spec.Banks {
		r := findResource(&st, setup.KindBank, b.ID)
		if r == nil {
			continue
		}
		c.Banks = append(c.Banks, runtimeBank(b, *r))
	}
	for _, p := range spec.Partners {
		r := findResource(&st, setup.KindPartner, p.ID)
		if r == nil || r.Partner == nil {
			continue
		}
		c.Partners = append(c.Partners, runtimePartner(p, *r.Partner, local.Partners[p.ID]))
	}
	if err := c.Finish(); err != nil {
		return nil, err
	}
	return c, nil
}

func runtimeBank(b setup.Bank, r setup.Resource) *config.Bank {
	state := b.State
	switch r.Phase {
	case setup.PhaseClosed:
		state = setup.StateClosed
	case setup.PhaseClosing, setup.PhaseTerminating:
		state = setup.StateSuspended
		if slices.Contains(r.Done, setup.FinalizerDecideMoney) {
			state = setup.StateClosed
		}
	}
	if state == setup.StateActive {
		state = ""
	}
	ob := b.OpenBanking
	return &config.Bank{
		ID:             b.ID,
		Name:           b.Name,
		Type:           b.Type,
		NIPCode:        orDefault(b.NIPCode, r.Allocated.NIPCode),
		NUBANCode:      orDefault(b.NUBANCode, r.Allocated.NUBANCode),
		HistoryDays:    typeHistoryDays[b.Type],
		Port:           r.Allocated.Port,
		APIPort:        r.Allocated.APIPort,
		Database:       r.Allocated.Database,
		SettlesThrough: b.SettlesThrough,
		State:          state,
		NetDebitCap:    b.NetDebitCap,
		Collateral:     b.Collateral,
		API:            config.BankAPI{ConsentMonths: ob.ConsentMonths, RefreshDays: ob.RefreshDays, DormantDays: ob.DormantDays, AssertionSeconds: ob.AssertionSeconds},
	}
}

func runtimePartner(p setup.Partner, k setup.PartnerKeys, l setup.PartnerLocal) *config.Partner {
	out := &config.Partner{ID: p.ID, Name: p.Name, LegalName: orDefault(p.LegalName, p.Name), Purpose: orDefault(p.Purpose, defaultPurpose), CertCN: k.CertCN, APIKeySHA256: k.APIKeySHA256, CertSHA256: k.CertSHA256, RedirectURIs: p.RedirectURIs, WebhookURL: p.WebhookURL}
	if len(l.RedirectURIs) > 0 {
		out.RedirectURIs = l.RedirectURIs
	}
	if l.WebhookURL != "" {
		out.WebhookURL = l.WebhookURL
	}
	return out
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}
