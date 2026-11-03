package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"text/template"

	"github.com/evolutionlabs-core/beerus/internal/config"
)

const composeFile = `name: beerus

x-beerus: &beerus
  image: beerus:local
  build: .
  user: "${BEERUS_UID:-10001}:${BEERUS_GID:-10001}"
  restart: on-failure
  environment: &env
    BEERUS_DIR: /state
    BEERUS_BIND: 0.0.0.0
    BEERUS_CORE_URL: http://sim:{{.ControlPort}}
    BEERUS_PARTNER_PORT: "{{.PartnerPort}}"
    BEERUS_WIDGET_PORT: "{{.WidgetPort}}"
    BEERUS_CONTROL_PORT: "{{.ControlPort}}"
    BEERUS_BANK_PORT: "{{.FirstBankPort}}"
  volumes:
    - ${BEERUS_STATE:-state}:/state

services:
  postgres:
    image: postgres:16-alpine
    environment:
      POSTGRES_PASSWORD: beerus
    volumes:
      - pgdata:/var/lib/postgresql/data
    healthcheck:
      test: ["CMD", "pg_isready", "-U", "postgres"]
      interval: 2s
      timeout: 3s
      retries: 30

  cbn:
    <<: *beerus
    command: ["run", "--participant", "cbn"]
    environment:
      <<: *env
      BEERUS_DATABASE_URL: postgres://postgres:beerus@postgres:5432/postgres?sslmode=disable
{{- if .NeedsPartner}}
      BEERUS_PARTNER_ID: ${BEERUS_PARTNER_ID:?set the partner app's ID}
      BEERUS_PARTNER_NAME: ${BEERUS_PARTNER_NAME:?set the partner app's name}
      BEERUS_PARTNER_LEGAL_NAME: ${BEERUS_PARTNER_LEGAL_NAME:?set the partner's legal name}
      BEERUS_PARTNER_PURPOSE: ${BEERUS_PARTNER_PURPOSE:?say what the partner uses the data for}
      BEERUS_PARTNER_REDIRECT_URI: ${BEERUS_PARTNER_REDIRECT_URI:?set the partner app's callback URL}
      BEERUS_PARTNER_WEBHOOK_URL: ${BEERUS_PARTNER_WEBHOOK_URL:-}
{{- end}}
    ports:
      - "127.0.0.1:${BEERUS_CBN_PORT:-7490}:7490"
    depends_on:
      postgres:
        condition: service_healthy
    healthcheck:
      test: ["CMD", "beerus", "health", "http://127.0.0.1:7490/health"]
      interval: 2s
      timeout: 3s
      retries: 30
{{range .Banks}}
  {{.ID}}:
    <<: *beerus
    command: ["run", "--participant", "{{.ID}}"]
    ports:
      - "127.0.0.1:{{.Port}}:{{.Port}}"
    networks:
      default:
        aliases:
          - {{.ID}}.localhost
    depends_on:
      cbn:
        condition: service_healthy
    healthcheck:
      test: ["CMD", "beerus", "health", "http://{{.ID}}.localhost:{{.Port}}/health"]
      interval: 2s
      timeout: 3s
      retries: 30
{{end}}
  sim:
    <<: *beerus
    command: ["run", "--participant", "sim"]
    ports:
      - "127.0.0.1:{{.PartnerPort}}:{{.PartnerPort}}"
      - "127.0.0.1:{{.WidgetPort}}:{{.WidgetPort}}"
      - "127.0.0.1:{{.ControlPort}}:{{.ControlPort}}"
    networks:
      default:
        aliases:
          - {{.ConnectHost}}
    depends_on:
{{- range .Banks}}
      {{.ID}}:
        condition: service_healthy
{{- end}}
    healthcheck:
      test: ["CMD", "beerus", "health", "--token-file", "/state/control-token", "http://127.0.0.1:{{.ControlPort}}/health"]
      interval: 2s
      timeout: 3s
      retries: 60

volumes:
  state:
  pgdata:
`

type composeData struct {
	PartnerPort, WidgetPort, ControlPort, FirstBankPort int
	ConnectHost                                         string
	NeedsPartner                                        bool
	Banks                                               []*config.Bank
}

func renderCompose(c *config.Config) ([]byte, error) {
	d := composeData{PartnerPort: c.Connect.PartnerPort, WidgetPort: c.Connect.WidgetPort, ControlPort: c.Connect.ControlPort, ConnectHost: c.Connect.Host, NeedsPartner: len(c.Partners) == 0}
	for _, b := range c.Banks {
		if !b.Closed() {
			d.Banks = append(d.Banks, b)
		}
	}
	if len(d.Banks) == 0 {
		return nil, fmt.Errorf("%s has no open banks to run", c.Path("beerus.yaml"))
	}
	d.FirstBankPort = d.Banks[0].Port
	var out bytes.Buffer
	if err := template.Must(template.New("compose").Parse(composeFile)).Execute(&out, d); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func cmdCompose(args []string) error {
	fs := flag.NewFlagSet("compose", flag.ExitOnError)
	dir := fs.String("dir", defaultDir(), "state directory")
	fs.Parse(args)
	c, err := config.Load(*dir)
	if err != nil {
		return err
	}
	out, err := renderCompose(c)
	if err != nil {
		return err
	}
	_, err = os.Stdout.Write(out)
	return err
}
