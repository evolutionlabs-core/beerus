package setup

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/config"
)

const (
	SpecFile    = "setup.yaml"
	OldSpecFile = "beerus.yaml"

	DefaultConnectHost = "connect.localhost"
	DefaultNIBSSPort   = 7481
	DefaultCBNPort     = 7446

	defaultLegalName  = "Beerus Connect Limited"
	defaultBrandName  = "Beerus Connect"
	defaultRegistryID = "OBR-T2-000001"
	defaultCACNumber  = "RC 0000001"
	defaultPurpose    = "to read your account details, balances and transactions"
)

var typeHistoryDays = map[string]int{"commercial": 730, "digital": 180, "microfinance": 365}

func refuseOld(dir string) error {
	if _, err := os.Stat(filepath.Join(dir, OldSpecFile)); err != nil {
		return nil
	}
	if _, err := os.Stat(filepath.Join(dir, StatusFile)); errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("%s: %w", dir, ErrOldSetup)
	}
	return nil
}

func Initialized(dir string) (bool, error) {
	if err := refuseOld(dir); err != nil {
		return false, err
	}
	_, err := os.Stat(filepath.Join(dir, StatusFile))
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, os.ErrNotExist):
		return false, nil
	default:
		return false, fmt.Errorf("look for %s: %w", StatusFile, err)
	}
}

func SpecSum(s Spec) (string, error) {
	raw, err := Export(s)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func portHolders(ln LocalConnect, st Status) map[int]string {
	holders := map[int]string{ln.PartnerPort: "connect.partner_port", ln.WidgetPort: "connect.widget_port", ln.ControlPort: "connect.control_port", ln.HealthPort: "connect.health_port"}
	for _, r := range st.Resources {
		if r.Kind == KindSystem {
			holders[r.Allocated.Port] = r.ID + " control API"
			if r.Allocated.APIPort != 0 {
				holders[r.Allocated.APIPort] = r.ID + " API"
			}
			continue
		}
		holders[r.Allocated.Port] = r.ID + " online banking"
		holders[r.Allocated.APIPort] = r.ID + " open banking API"
	}
	return holders
}

func nibssPort(ln LocalConnect, st Status) (int, error) {
	port := ln.NIBSSPort
	if port == 0 {
		port = DefaultNIBSSPort
	}
	if holder, taken := portHolders(ln, st)[port]; taken {
		return 0, fmt.Errorf("NIBSS port %d is already used by %s; set connect.nibss_port in %s to a free port", port, holder, LocalFile)
	}
	return port, nil
}

func cbnPort(ln LocalConnect, nibss int, st Status) (int, error) {
	own := Status{Resources: slices.DeleteFunc(slices.Clone(st.Resources), func(r Resource) bool {
		return r.Kind == KindSystem && r.ID == SystemCBN
	})}
	holders := portHolders(ln, own)
	holders[nibss] = "connect.nibss_port"
	if ln.CBNPort != 0 {
		if holder, taken := holders[ln.CBNPort]; taken {
			return 0, fmt.Errorf("CBN port %d is already used by %s; set connect.cbn_port in %s to a free port", ln.CBNPort, holder, LocalFile)
		}
		return ln.CBNPort, nil
	}
	if r := FindResource(&st, KindSystem, SystemCBN); r != nil && r.Allocated.APIPort != 0 {
		return r.Allocated.APIPort, nil
	}
	port := DefaultCBNPort
	for {
		if _, taken := holders[port]; !taken {
			return port, nil
		}
		port++
	}
}

func LoadConfig(dir string) (*config.Config, error) {
	if err := refuseOld(dir); err != nil {
		return nil, err
	}
	st, err := LoadStatus(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("%s holds no Beerus setup; run beerus init to make one", dir)
		}
		return nil, err
	}
	local, err := LoadLocal(dir)
	if err != nil {
		return nil, err
	}
	return BuildConfig(dir, st, local)
}

func BuildConfig(dir string, st Status, local Local) (*config.Config, error) {
	spec := st.Applied.Spec
	def := DefaultSpec(st.Applied.Metadata.Name).Spec
	tsqAfter, err := time.ParseDuration(cmp.Or(spec.NIP.TSQAfter, def.NIP.TSQAfter))
	if err != nil {
		return nil, fmt.Errorf("nip.tsq_after %q: %w", spec.NIP.TSQAfter, err)
	}
	ln := local.Connect
	nibss, err := nibssPort(ln, st)
	if err != nil {
		return nil, err
	}
	cbn, err := cbnPort(ln, nibss, st)
	if err != nil {
		return nil, err
	}
	c := &config.Config{
		Dir:         dir,
		DatabaseURL: local.DatabaseURL,
		NIPSessions: spec.NIP.Sessions,
		TSQAfter:    tsqAfter,
		SetupKey:    st.SetupKey,
		Ports:       config.Ports{APIOffset: local.Ports.APIOffset, Window: local.Ports.Window},
		Connect: config.Connect{
			Host:         cmp.Or(ln.Host, DefaultConnectHost),
			PartnerPort:  ln.PartnerPort,
			WidgetPort:   ln.WidgetPort,
			WidgetTLS:    ln.WidgetTLS,
			ControlPort:  ln.ControlPort,
			HealthPort:   ln.HealthPort,
			NIBSSPort:    nibss,
			CBNPort:      cbn,
			SyncInterval: ln.SyncInterval,
			RefreshEvery: ln.RefreshEvery,
			LegalName:    cmp.Or(spec.Connect.LegalName, defaultLegalName),
			BrandName:    cmp.Or(spec.Connect.BrandName, defaultBrandName),
			RegistryID:   cmp.Or(spec.Connect.RegistryID, defaultRegistryID),
			CACNumber:    cmp.Or(spec.Connect.CACNumber, defaultCACNumber),
		},
	}
	if r := FindResource(&st, KindSystem, SystemNIBSS); r != nil {
		c.Connect.NIBSSControlPort = r.Allocated.Port
	}
	if r := FindResource(&st, KindSystem, SystemCBN); r != nil {
		c.Connect.CBNControlPort = r.Allocated.Port
	}
	if len(spec.Holidays.Add)+len(spec.Holidays.Remove) > 0 {
		c.Holidays = &config.Holidays{Add: spec.Holidays.Add, Remove: spec.Holidays.Remove}
	}
	for _, b := range spec.Banks {
		r := FindResource(&st, KindBank, b.ID)
		if r == nil {
			continue
		}
		c.Banks = append(c.Banks, RuntimeBank(b, *r))
	}
	for _, p := range spec.Partners {
		r := FindResource(&st, KindPartner, p.ID)
		if r == nil || r.Partner == nil {
			continue
		}
		c.Partners = append(c.Partners, RuntimePartner(p, *r.Partner, local.Partners[p.ID]))
	}
	if err := c.Finish(); err != nil {
		return nil, err
	}
	return c, nil
}

func RuntimeBank(b Bank, r Resource) *config.Bank {
	state := b.State
	switch r.Phase {
	case config.PhaseClosed:
		state = StateClosed
	case config.PhaseClosing, config.PhaseTerminating:
		state = StateSuspended
		if slices.Contains(r.Done, FinalizerDecideMoney) {
			state = StateClosed
		}
	}
	if state == StateActive {
		state = ""
	}
	ob := b.OpenBanking
	return &config.Bank{
		ID:             b.ID,
		Name:           b.Name,
		Type:           b.Type,
		NIPCode:        cmp.Or(b.NIPCode, r.Allocated.NIPCode),
		NUBANCode:      cmp.Or(b.NUBANCode, r.Allocated.NUBANCode),
		HistoryDays:    typeHistoryDays[b.Type],
		Port:           r.Allocated.Port,
		APIPort:        r.Allocated.APIPort,
		Database:       r.Allocated.Database,
		SettlesThrough: b.SettlesThrough,
		State:          state,
		NetDebitCap:    b.NetDebitCap,
		Collateral:     b.Collateral,
		API:            config.BankAPI{ConsentMonths: ob.ConsentMonths, RefreshDays: ob.RefreshDays, DormantDays: ob.DormantDays, AssertionSeconds: ob.AssertionSeconds, AccessSeconds: ob.AccessSeconds},
	}
}

func RuntimePartner(p Partner, k PartnerKeys, l PartnerLocal) *config.Partner {
	out := &config.Partner{ID: p.ID, Name: p.Name, LegalName: cmp.Or(p.LegalName, p.Name), Purpose: cmp.Or(p.Purpose, defaultPurpose), CertCN: k.CertCN, APIKeySHA256: k.APIKeySHA256, CertSHA256: k.CertSHA256, RedirectURIs: p.RedirectURIs, WebhookURL: p.WebhookURL}
	if len(l.RedirectURIs) > 0 {
		out.RedirectURIs = l.RedirectURIs
	}
	if l.WebhookURL != "" {
		out.WebhookURL = l.WebhookURL
	}
	return out
}
