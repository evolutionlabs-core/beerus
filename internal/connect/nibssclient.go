package connect

import (
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/bank/server"
	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/pki"
)

const (
	nibssCallBudget       = 15 * time.Second
	nibssIdleTimeout      = 90 * time.Second
	nibssIdleConnsPerHost = 4
	nibssAway             = "NIBSS is not available right now. Try again soon."
	nibssHost             = "127.0.0.1"
)

func (a *App) nibssClient() (server.NIBSSClient, error) {
	a.mu.Lock()
	hc, port := a.nibssHTTP, a.cfg.Connect.NIBSSPort
	a.mu.Unlock()
	if hc == nil {
		built, err := a.newNIBSSHTTP()
		if err != nil {
			return server.NIBSSClient{}, fmt.Errorf("build the NIBSS client: %w", err)
		}
		a.mu.Lock()
		if a.nibssHTTP == nil {
			a.nibssHTTP = built
		}
		hc = a.nibssHTTP
		a.mu.Unlock()
	}
	return server.NIBSSClient{HTTP: hc, Base: "https://" + net.JoinHostPort(nibssHost, strconv.Itoa(port)), Away: nibssAway}, nil
}

func (a *App) newNIBSSHTTP() (*http.Client, error) {
	pool, err := pki.LoadPool(a.cfg.Path("ca", "root.crt"))
	if err != nil {
		return nil, err
	}
	certPath, keyPath := a.cfg.Path("connect", "client.crt"), a.cfg.Path("connect", "client.key")
	tc := &tls.Config{
		RootCAs:    pool,
		ServerName: config.NIBSSHost,
		MinVersion: tls.VersionTLS12,
		GetClientCertificate: func(*tls.CertificateRequestInfo) (*tls.Certificate, error) {
			pair, err := tls.LoadX509KeyPair(certPath, keyPath)
			if err != nil {
				return nil, fmt.Errorf("load control plane client certificate: %w", err)
			}
			return &pair, nil
		},
	}
	tr := &http.Transport{TLSClientConfig: tc, IdleConnTimeout: nibssIdleTimeout, MaxIdleConnsPerHost: nibssIdleConnsPerHost}
	return &http.Client{Timeout: nibssCallBudget, Transport: tr}, nil
}
