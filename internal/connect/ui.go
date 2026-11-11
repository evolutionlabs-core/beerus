package connect

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"strconv"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/clock"
)

//go:embed assets
var assetFS embed.FS

var cssURL = func() string {
	b, err := assetFS.ReadFile("assets/beerus.css")
	if err != nil {
		return "/assets/beerus.css"
	}
	sum := sha256.Sum256(b)
	return "/assets/beerus.css?v=" + hex.EncodeToString(sum[:6])
}()

func assetHandler() http.Handler {
	sub, _ := fs.Sub(assetFS, "assets")
	files := http.FileServer(http.FS(sub))
	return http.StripPrefix("/assets/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=3600")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		files.ServeHTTP(w, r)
	}))
}

const pageCSP = "default-src 'none'; script-src 'self'; style-src 'self'; font-src 'self'; img-src 'self' data:; form-action 'self' http: https:; frame-ancestors 'none'; base-uri 'none'"

func setPageHeaders(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", pageCSP)
	w.Header().Set("Referrer-Policy", "same-origin")
}

func formatNaira(minor int64) string {
	sign := ""
	if minor < 0 {
		sign, minor = "−", -minor
	}
	n := strconv.FormatInt(minor/100, 10)
	out := make([]byte, 0, len(n)+len(n)/3)
	for i := range n {
		if i > 0 && (len(n)-i)%3 == 0 {
			out = append(out, ',')
		}
		out = append(out, n[i])
	}
	return fmt.Sprintf("%s₦%s.%02d", sign, out, minor%100)
}

func signedNaira(minor int64) string {
	if minor > 0 {
		return "+" + formatNaira(minor)
	}
	return formatNaira(minor)
}

const themeToggleHTML = `<button type="button" class="theme-toggle" aria-label="Switch theme"><svg viewBox="0 0 16 16" width="16" height="16" fill="none" aria-hidden="true" class="theme-sun"><circle cx="8" cy="8" r="2.8" stroke="currentColor" stroke-width="1.3"/><path d="M8 1.5v1.6M8 12.9v1.6M1.5 8h1.6M12.9 8h1.6M3.4 3.4l1.1 1.1M11.5 11.5l1.1 1.1M3.4 12.6l1.1-1.1M11.5 4.5l1.1-1.1" stroke="currentColor" stroke-width="1.3" stroke-linecap="round"/></svg><svg viewBox="0 0 16 16" width="16" height="16" fill="none" aria-hidden="true" class="theme-moon"><path d="M13.2 9.4A5.6 5.6 0 0 1 6.6 2.8a5.6 5.6 0 1 0 6.6 6.6Z" stroke="currentColor" stroke-width="1.3" stroke-linejoin="round"/></svg></button>`

var uiFuncs = template.FuncMap{
	"count": func(n int, one, many string) string {
		if n == 1 {
			return fmt.Sprintf("%d %s", n, one)
		}
		return fmt.Sprintf("%d %s", n, many)
	},
	"themeToggle": func() template.HTML { return template.HTML(themeToggleHTML) },
	"naira":       formatNaira,
	"signed":      signedNaira,
	"when": func(t time.Time) string {
		if t.IsZero() {
			return "—"
		}
		return t.In(clock.Lagos).Format("2 Jan 2006, 15:04")
	},
	"day": func(t time.Time) string {
		if t.IsZero() {
			return "—"
		}
		return t.In(clock.Lagos).Format("2 Jan 2006")
	},
	"short": func(s string, n int) string {
		if len(s) <= n {
			return s
		}
		return s[:n] + "…"
	},
	"initial": func(s string) string {
		if s == "" {
			return "?"
		}
		return s[len(s)-1:]
	},
	"statusClass": func(s string) string {
		switch s {
		case "active", "posted", "delivered", "succeeded":
			return "ok"
		case "syncing", "pending", "institution_down", "sending":
			return "warn"
		case "revoked", "expired", "reauth_required", "error", "failed", "no_endpoint":
			return "bad"
		}
		return ""
	},
	"human": func(s string) string {
		switch s {
		case "reauth_required":
			return "Needs reauthorisation"
		case "institution_down":
			return "Bank unavailable"
		case "no_endpoint":
			return "No endpoint"
		case "nip":
			return "NIP"
		}
		if s == "" {
			return s
		}
		return string(s[0]-32) + s[1:]
	},
	"add":   func(a, b int) int { return a + b },
	"css":   func() string { return cssURL },
	"steps": func() []int { return []int{0, 1, 2, 3} },
}
