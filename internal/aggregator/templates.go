package aggregator

import (
	"html/template"

	"github.com/evolutionlabs-core/beerus/internal/view"
)

var pages = template.Must(template.New("").Funcs(view.Funcs()).Funcs(template.FuncMap{
	"logo": func() template.HTML { return template.HTML(view.LogoSVG) },
}).Parse(`
{{define "head"}}<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="color-scheme" content="dark light">
<title>{{.Title}}</title>
<link rel="stylesheet" href="{{css}}">
<script src="/assets/theme.js"></script>
<link rel="icon" href="/assets/icon.svg" type="image/svg+xml">
</head>
<body class="min-h-screen">
<div class="fixed top-3 right-4">{{themeToggle}}</div>
<main class="flex min-h-screen flex-col items-center px-4 pt-14 pb-10 sm:pt-20 {{.BankClass}}">
<div class="w-full max-w-[420px]">
<header class="mb-6 flex items-center gap-3">
{{if .Mark}}<span class="grid size-9 place-items-center rounded-[10px] border border-line-strong bg-raised text-[15px] font-medium text-bank">{{.Mark}}</span>{{else}}<span class="grid size-9 place-items-center rounded-[10px] border border-line-strong bg-raised text-ink">{{logo}}</span>{{end}}
<div class="leading-tight"><div class="text-[14px]">{{.Brand}}</div><div class="text-[12.5px] text-muted">{{.Ctx}}</div></div>
</header>
<section class="rounded-2xl border border-line bg-panel p-7">
{{end}}

{{define "foot"}}</section>
<footer class="mt-5 flex items-center justify-between gap-3 px-1 text-[12px] text-faint">
<span class="flex items-center gap-1.5"><svg viewBox="0 0 16 16" class="size-3.5" fill="none" aria-hidden="true"><rect x="3" y="7" width="10" height="7" rx="1.5" stroke="currentColor" stroke-width="1.2"/><path d="M5.5 7V5a2.5 2.5 0 0 1 5 0v2" stroke="currentColor" stroke-width="1.2"/></svg>Secured by {{.ConnectBrand}}</span>
{{if .PartnerName}}<span>For {{.PartnerName}}</span>{{end}}
</footer>
</div>
</main>
</body>
</html>{{end}}

{{define "steps"}}<div class="mb-6 flex gap-1.5" aria-label="Step {{.}} of 4">{{range $i, $s := steps}}<i class="h-0.5 flex-1 rounded-full {{if lt $i $}}bg-ink{{else}}bg-line-strong{{end}}"></i>{{end}}</div>{{end}}

{{define "error"}}{{if .Error}}<p class="mt-4 rounded-[10px] border border-neg/30 bg-neg/10 px-3 py-2.5 text-[13px] text-neg" role="alert">{{.Error}}</p>{{end}}{{end}}

{{define "cancel"}}<form method="post" action="/link/cancel" class="mt-2"><input type="hidden" name="csrf" value="{{.CSRF}}"><button type="submit" class="btn w-full text-muted hover:text-ink">Cancel</button></form>{{end}}

{{define "choose"}}{{template "head" .}}
{{template "steps" 1}}
<h1 class="text-[22px] leading-snug">Connect your bank</h1>
<p class="mt-2 text-[13.5px] leading-relaxed text-muted">{{.PartnerName}} uses {{.ConnectBrand}} to read your account details, balances and transactions. You approve access at your bank, and you can withdraw it at any time.</p>
<div class="mt-6 space-y-2">
{{range .Institutions}}<form method="post" action="/link/institution">
<input type="hidden" name="csrf" value="{{$.CSRF}}"><input type="hidden" name="institution" value="{{.ID}}">
<button type="submit" class="{{.ID}} group flex w-full cursor-pointer items-center gap-3 rounded-xl border border-line px-3.5 py-3 text-left transition-colors hover:border-line-strong hover:bg-raised">
<span class="grid size-8 place-items-center rounded-lg border border-line-strong bg-raised text-[13px] font-medium text-bank">{{initial .Name}}</span>
<span class="flex-1"><span class="block text-[14px]">{{.Name}}</span><span class="block text-[12.5px] text-muted">{{if eq .Style "digital"}}Digital bank{{else}}Commercial bank{{end}}</span></span>
<svg viewBox="0 0 16 16" class="size-4 text-faint transition-transform group-hover:translate-x-0.5" fill="none" aria-hidden="true"><path d="m6 3.5 4.5 4.5L6 12.5" stroke="currentColor" stroke-width="1.4" stroke-linecap="round" stroke-linejoin="round"/></svg>
</button></form>{{end}}
</div>
{{template "cancel" .}}
{{template "foot" .}}{{end}}

{{define "message"}}{{template "head" .}}
<h1 class="text-[22px] leading-snug">{{.Heading}}</h1>
<p class="mt-2 text-[13.5px] leading-relaxed text-muted">{{.Message}}</p>
{{template "foot" .}}{{end}}
`))
