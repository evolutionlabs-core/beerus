package fleet

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"slices"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/config"
)

const (
	syncEvery   = 250 * time.Millisecond
	readTimeout = 10 * time.Second
	banksPath   = "/banks"
	maxBody     = 4 << 20
)

type Runner interface {
	Start(id string) (stop func(), done <-chan struct{})
}

type bank struct {
	ID       string `json:"id"`
	Phase    string `json:"phase"`
	Port     int    `json:"port"`
	APIPort  int    `json:"api_port"`
	Database string `json:"database"`
	Steps    []struct {
		ID   string `json:"id"`
		Done bool   `json:"done"`
	} `json:"steps"`
}

func (b bank) startable() bool {
	switch b.Phase {
	case config.PhaseActive, config.PhaseSuspended, config.PhaseClosing, config.PhaseTerminating:
		return true
	case config.PhaseProvisioning, config.PhaseRepairing:
		for _, s := range b.Steps {
			if s.ID == config.StepCBN {
				return s.Done
			}
		}
	}
	return false
}

func (b bank) fingerprint() string {
	return fmt.Sprintf("%d/%d/%s", b.Port, b.APIPort, b.Database)
}

type member struct {
	print string
	stop  func()
	done  <-chan struct{}
}

type Fleet struct {
	url      string
	token    string
	client   *http.Client
	runner   Runner
	log      *slog.Logger
	banks    map[string]*member
	order    []string
	stopping map[string]*member
}

func New(controlURL, token string, r Runner, log *slog.Logger) *Fleet {
	return &Fleet{
		url:      controlURL,
		token:    token,
		client:   &http.Client{Timeout: readTimeout},
		runner:   r,
		log:      log,
		banks:    map[string]*member{},
		stopping: map[string]*member{},
	}
}

func (f *Fleet) Run(ctx context.Context) {
	tick := time.NewTicker(syncEvery)
	defer tick.Stop()
	lastErr := ""
	for {
		banks, err := f.read(ctx)
		switch {
		case err == nil:
			lastErr = ""
			f.sync(banks)
		case err.Error() != lastErr && ctx.Err() == nil:
			f.log.Warn("core is not readable; keeping the banks as they are", "err", err)
			lastErr = err.Error()
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

func (f *Fleet) read(ctx context.Context) ([]bank, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, f.url+banksPath, nil)
	if err != nil {
		return nil, fmt.Errorf("read banks: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+f.token)
	resp, err := f.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("read banks: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil {
		return nil, fmt.Errorf("read banks: %w", err)
	}
	if len(raw) > maxBody {
		return nil, fmt.Errorf("read banks: the answer is longer than %d bytes", maxBody)
	}
	if resp.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("read banks: core answered %s", resp.Status)
	}
	var list struct {
		Data []bank `json:"data"`
	}
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, fmt.Errorf("read banks: %w", err)
	}
	if list.Data == nil {
		return nil, errors.New("read banks: the list is missing")
	}
	return list.Data, nil
}

func (f *Fleet) sync(banks []bank) {
	f.reap()
	listed := map[string]bool{}
	for _, b := range banks {
		listed[b.ID] = true
		running := f.banks[b.ID]
		switch {
		case b.Phase == config.PhaseClosed:
			f.drop(b.ID)
		case running != nil && running.print != b.fingerprint():
			f.drop(b.ID)
		case running == nil && b.startable():
			f.bring(b.ID, b.fingerprint())
		}
	}
	for _, id := range slices.Sorted(maps.Keys(f.banks)) {
		if !listed[id] {
			f.drop(id)
		}
	}
}

func (f *Fleet) Close() {
	for _, id := range f.order {
		f.banks[id].stop()
	}
	for _, id := range slices.Backward(slices.Clone(f.order)) {
		<-f.banks[id].done
	}
	for _, m := range f.stopping {
		<-m.done
	}
	f.banks = map[string]*member{}
	f.order = nil
	f.stopping = map[string]*member{}
}

func (f *Fleet) bring(id, print string) {
	if _, busy := f.stopping[id]; busy {
		return
	}
	f.log.Info("starting participant", "participant", id)
	stop, done := f.runner.Start(id)
	f.banks[id] = &member{print: print, stop: stop, done: done}
	f.order = append(f.order, id)
}

func (f *Fleet) drop(id string) {
	m := f.banks[id]
	if m == nil {
		return
	}
	f.log.Info("stopping participant", "participant", id)
	m.stop()
	delete(f.banks, id)
	f.order = slices.DeleteFunc(f.order, func(o string) bool { return o == id })
	f.stopping[id] = m
}

func (f *Fleet) reap() {
	for id, m := range f.stopping {
		select {
		case <-m.done:
			delete(f.stopping, id)
		default:
		}
	}
}
