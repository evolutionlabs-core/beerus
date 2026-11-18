package seed

import (
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"go.yaml.in/yaml/v3"
	"os"
	"path/filepath"

	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/countrypack"
	"github.com/evolutionlabs-core/beerus/internal/setup"
	"github.com/evolutionlabs-core/beerus/internal/state"
)

func Init(dir string, spec setup.Spec, local setup.Local, today clock.Date) error {
	ok, err := setup.Initialized(dir)
	if err != nil {
		return err
	}
	if ok {
		return fmt.Errorf("%s already holds a Beerus setup", dir)
	}
	if errs := setup.Validate(spec); len(errs) > 0 {
		return errors.Join(errs...)
	}
	pack, err := countrypack.Nigeria()
	if err != nil {
		return fmt.Errorf("load the country pack: %w", err)
	}
	plan := setup.Build(pack, spec, setup.Status{}, nil, today)
	if len(plan.Errors) > 0 {
		return errors.Join(plan.Errors...)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	if err := writeYAML(filepath.Join(dir, setup.LocalFile), local); err != nil {
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
		{Kind: setup.KindSystem, ID: setup.SystemCBN, Phase: config.PhaseProvisioning},
		{Kind: setup.KindSystem, ID: setup.SystemNIBSS, Phase: config.PhaseProvisioning},
	}
	cfg, err := setup.BuildConfig(dir, st, local)
	if err != nil {
		return err
	}
	if err := allocateCBN(cfg, &st); err != nil {
		return err
	}
	if cfg, err = setup.BuildConfig(dir, st, local); err != nil {
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
	if cfg, err = setup.BuildConfig(dir, st, local); err != nil {
		return err
	}
	if err := config.Prepare(cfg, clock.New().Now()); err != nil {
		return err
	}
	recordCertPrints(cfg, &st)
	if st.AppliedSHA256, err = setup.SpecSum(st.Applied); err != nil {
		return err
	}
	raw, err := setup.Export(spec)
	if err != nil {
		return err
	}
	if err := config.WriteSecret(filepath.Join(dir, setup.SpecFile), string(raw)); err != nil {
		return fmt.Errorf("write %s: %w", setup.SpecFile, err)
	}
	if _, err := state.Create(dir, st); err != nil {
		return err
	}
	return nil
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

func allocateCBN(cfg *config.Config, st *setup.Status) error {
	used := map[int]bool{cfg.Connect.PartnerPort: true, cfg.Connect.WidgetPort: true, cfg.Connect.ControlPort: true, cfg.Connect.HealthPort: true, cfg.Connect.NIBSSPort: true, cfg.Connect.CBNPort: true}
	for _, r := range st.Resources {
		used[r.Allocated.Port], used[r.Allocated.APIPort] = true, true
	}
	control, err := cfg.AllocatePort(cfg.Connect.CBNPort+1, used, anyPort)
	if err != nil {
		return fmt.Errorf("CBN control port: %w", err)
	}
	r := state.EnsureResource(st, setup.KindSystem, setup.SystemCBN)
	r.Allocated.Port, r.Allocated.APIPort = control, cfg.Connect.CBNPort
	r.Owned = append(r.Owned, setup.Owned{Kind: state.OwnedPort, Name: fmt.Sprint(control)}, setup.Owned{Kind: state.OwnedPort, Name: fmt.Sprint(cfg.Connect.CBNPort)})
	return nil
}

func anyPort(int) bool { return true }

func allocateBanks(cfg *config.Config, st *setup.Status) error {
	used := map[int]bool{cfg.Connect.PartnerPort: true, cfg.Connect.WidgetPort: true, cfg.Connect.ControlPort: true, cfg.Connect.HealthPort: true, cfg.Connect.NIBSSPort: true, cfg.Connect.CBNPort: true, cfg.Connect.CBNControlPort: true}
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
			Phase:      config.PhaseProvisioning,
			Allocated:  setup.Allocation{NIPCode: b.NIPCode, NUBANCode: b.NUBANCode, Database: config.DatabaseName(cfg.Dir, b.ID), Port: port, APIPort: api},
			Steps:      map[string]string{config.StepAllocate: state.StepDone},
		}
		r.Owned = []setup.Owned{{Kind: state.OwnedPort, Name: fmt.Sprint(port)}, {Kind: state.OwnedPort, Name: fmt.Sprint(api)}}
		st.Resources = append(st.Resources, r)
	}
	return nil
}

func issuePartnerKeys(dir string, partners []setup.Partner) ([]setup.Resource, error) {
	var out []setup.Resource
	for _, p := range partners {
		key, err := config.WriteAPIKey(dir, p.ID)
		if err != nil {
			return nil, err
		}
		out = append(out, setup.Resource{
			Kind:               setup.KindPartner,
			ID:                 p.ID,
			Generation:         1,
			ObservedGeneration: 1,
			Phase:              config.PhaseActive,
			Partner:            &setup.PartnerKeys{CertCN: p.ID, APIKeySHA256: config.HashKey(key)},
			Owned:              []setup.Owned{{Kind: state.OwnedDir, Name: filepath.Join("partners", p.ID)}},
		})
	}
	return out, nil
}

func recordCertPrints(cfg *config.Config, st *setup.Status) {
	for _, p := range cfg.Partners {
		if r := setup.FindResource(st, setup.KindPartner, p.ID); r != nil && r.Partner != nil {
			r.Partner.CertSHA256 = p.CertSHA256
		}
	}
}

func newSetupKey() (uint64, error) {
	var b [8]byte
	for {
		if _, err := rand.Read(b[:]); err != nil {
			return 0, fmt.Errorf("make setup key: %w", err)
		}
		if k := binary.BigEndian.Uint64(b[:]); k != 0 {
			return k, nil
		}
	}
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
