package connect

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"strconv"
	"time"
)

var retrySchedule = []time.Duration{0, 5 * time.Second, 30 * time.Second, 2 * time.Minute, 10 * time.Minute, 30 * time.Minute, 2 * time.Hour, 5 * time.Hour, 10 * time.Hour, 14 * time.Hour, 20 * time.Hour, 20 * time.Hour}

func jitter(d time.Duration) time.Duration {
	if d <= 0 {
		return d
	}
	return d + time.Duration(rand.Int64N(int64(d)/5+1)) - d/10
}

func Sign(key ed25519.PrivateKey, id string, ts int64, body []byte) string {
	msg := []byte(id + "." + strconv.FormatInt(ts, 10) + ".")
	msg = append(msg, body...)
	return "v1a," + base64.StdEncoding.EncodeToString(ed25519.Sign(key, msg))
}

func (a *App) dueDeliveries() []Delivery {
	real := a.clock.Real()
	out := []Delivery{}
	for _, d := range a.st.Deliveries {
		if d.Status == "pending" && !real.Before(d.NextReal) {
			d.Status = "sending"
			out = append(out, *d)
		}
	}
	return out
}

func (a *App) deliver(d Delivery) {
	p := a.partnerOf(d.PartnerID)
	status, errText := 0, ""
	if p == nil || p.WebhookURL == "" {
		errText = "no webhook endpoint"
	} else {
		ts := a.clock.Real().Unix()
		req, err := http.NewRequest(http.MethodPost, p.WebhookURL, bytes.NewReader(d.Body))
		if err == nil {
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("User-Agent", "Beerus-Connect-Webhooks/1")
			req.Header.Set("webhook-id", d.ID)
			req.Header.Set("webhook-timestamp", strconv.FormatInt(ts, 10))
			req.Header.Set("webhook-signature", Sign(a.key.Private, d.ID, ts, d.Body))
			resp, err := a.client.Do(req)
			if err == nil {
				status = resp.StatusCode
				_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
				resp.Body.Close()
			} else {
				errText = err.Error()
			}
		} else {
			errText = err.Error()
		}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, x := range a.st.Deliveries {
		if x.ID != d.ID {
			continue
		}
		x.Attempts++
		x.LastStatus = status
		x.LastError = errText
		switch {
		case status >= 200 && status < 300:
			x.Status = "delivered"
			x.LastError = ""
		case p == nil || p.WebhookURL == "":
			x.Status = "no_endpoint"
		case x.Attempts >= len(retrySchedule):
			x.Status = "failed"
		default:
			x.Status = "pending"
			x.NextReal = a.clock.Real().Add(jitter(retrySchedule[x.Attempts]))
			if errText == "" {
				x.LastError = fmt.Sprintf("HTTP %d", status)
			}
		}
		a.dirty()
	}
}

func (a *App) Replay(id string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, d := range a.st.Deliveries {
		if d.ID == id {
			d.Status = "pending"
			d.Attempts = 0
			d.NextReal = a.clock.Real()
			a.dirty()
			a.poke()
			return nil
		}
	}
	return errNotFound
}
