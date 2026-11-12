package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"text/template"

	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/connect"
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
    BEERUS_DATABASE_URL: postgres://postgres:beerus@postgres:5432/postgres?sslmode=disable
    BEERUS_CORE_URL: http://core:{{.ControlPort}}
    BEERUS_PARTNER_PORT: "{{.PartnerPort}}"
    BEERUS_WIDGET_PORT: "{{.WidgetPort}}"
    BEERUS_CONTROL_PORT: "{{.ControlPort}}"
    BEERUS_HEALTH_PORT: "{{.HealthPort}}"
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
      BEERUS_SETUP_FILE: ${BEERUS_SETUP_FILE:-}
{{- if .NeedsPartner}}
      BEERUS_PARTNER_ID: ${BEERUS_PARTNER_ID:?set the partner app's ID}
      BEERUS_PARTNER_NAME: ${BEERUS_PARTNER_NAME:?set the partner app's name}
      BEERUS_PARTNER_LEGAL_NAME: ${BEERUS_PARTNER_LEGAL_NAME:?set the partner's legal name}
      BEERUS_PARTNER_PURPOSE: ${BEERUS_PARTNER_PURPOSE:?say what the partner uses the data for}
      BEERUS_PARTNER_REDIRECT_URI: ${BEERUS_PARTNER_REDIRECT_URI:?set the partner app's callback URL}
      BEERUS_PARTNER_WEBHOOK_URL: ${BEERUS_PARTNER_WEBHOOK_URL:-}
{{- end}}
    ports:
      - "127.0.0.1:{{.HealthPort}}:{{.HealthPort}}"
    depends_on:
      postgres:
        condition: service_healthy
    healthcheck:
      test: ["CMD", "beerus", "health", "http://127.0.0.1:{{.HealthPort}}/health"]
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
  core:
    <<: *beerus
    command: ["run", "--participant", "core"]
    ports:
      - "127.0.0.1:{{.PartnerPort}}:{{.PartnerPort}}"
      - "127.0.0.1:{{.WidgetPort}}:{{.WidgetPort}}"
      - "127.0.0.1:{{.ControlPort}}:{{.ControlPort}}"
    networks:
      default:
        aliases:
          - {{.ConnectHost}}
{{- if .Banks}}
    depends_on:
{{- range .Banks}}
      {{.ID}}:
        condition: service_healthy
{{- end}}
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
	PartnerPort, WidgetPort, ControlPort, HealthPort int
	ConnectHost                                      string
	NeedsPartner                                     bool
	Banks                                            []*config.Bank
}

func renderCompose(c *config.Config) ([]byte, error) {
	d := composeData{PartnerPort: c.Connect.PartnerPort, WidgetPort: c.Connect.WidgetPort, ControlPort: c.Connect.ControlPort, HealthPort: c.Connect.HealthPort, ConnectHost: c.Connect.Host, NeedsPartner: len(c.Partners) == 0}
	for _, b := range c.Banks {
		if !b.Closed() {
			d.Banks = append(d.Banks, b)
		}
	}
	if d.HealthPort <= 0 {
		return nil, fmt.Errorf("%s has no connect.health_port; set it to the port the cbn container listens on", c.Path(connect.LocalFile))
	}
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
	c, err := connect.LoadConfig(*dir)
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
