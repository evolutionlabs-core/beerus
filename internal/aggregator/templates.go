package aggregator

import (
	"html/template"

	"github.com/evolutionlabs-core/beerus/internal/view"
)

var pages = template.Must(template.New("").Funcs(view.Funcs()).Funcs(template.FuncMap{
	"logo": func() template.HTML { return template.HTML(view.LogoSVG) },
}).Parse(`
{{define "head"}}<!doctype html>
<html lang="en" data-theme="light">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="color-scheme" content="dark light">
<title>{{.Title}}</title>
<link rel="stylesheet" href="{{css}}">
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

{{define "error"}}{{if .Error}}<p class="mt-4 rounded-[10px] border border-neg/30 bg-neg/10 px-3 py-2.5 text-[13px] text-neg" role="alert">{{.Error}}</p>{{end}}{{end}}

{{define "choose"}}<!doctype html>
<html lang="en" data-theme="light">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="color-scheme" content="dark light">
<title>{{.Title}}</title>
<link rel="stylesheet" href="{{css}}">
<link rel="icon" href="/assets/icon.svg" type="image/svg+xml">
</head>
<body class="min-h-screen" data-overlay="{{.Overlay}}" data-fixed="{{.Fixed}}" data-parent="{{.Parent}}" data-app="{{.PartnerName}}">
<form method="post" action="/link/cancel"><input type="hidden" name="csrf" value="{{.CSRF}}"><button type="submit" id="close" class="absolute top-3 right-3 grid size-11 cursor-pointer place-items-center rounded-full text-muted hover:bg-hover hover:text-ink" aria-label="Close"><svg viewBox="0 0 16 16" class="size-4" fill="none" aria-hidden="true"><path d="m3.5 3.5 9 9m0-9-9 9" stroke="currentColor" stroke-width="1.4" stroke-linecap="round"/></svg></button></form>
<main class="mx-auto flex min-h-screen max-w-[420px] flex-col p-6">
<div id="list" class="flex flex-1 flex-col pt-8">
<h1 class="text-[24px] leading-tight font-semibold">Pick your bank</h1>
<input id="search" type="search" autocomplete="off" placeholder="Search banks" aria-label="Search banks" class="mt-5 h-12 w-full rounded-[10px] border border-line-strong bg-transparent px-3.5 text-[15px] text-ink placeholder:text-faint focus-visible:outline-2 focus-visible:outline-offset-1 focus-visible:outline-line-strong">
<div class="mt-3">
{{range .Institutions}}<form method="post" action="/link/institution"{{if $.Overlay}} target="beerus_bank"{{end}} data-bank data-id="{{.ID}}" data-name="{{.Name}}">
<input type="hidden" name="csrf" value="{{$.CSRF}}"><input type="hidden" name="institution" value="{{.ID}}">
<button type="submit" class="group flex h-16 w-full cursor-pointer items-center gap-3 border-b border-line text-left">
<span class="grid size-10 shrink-0 place-items-center rounded-[10px] bg-hover text-[15px] font-medium">{{initial .Name}}</span>
<span class="flex-1 text-[16px] font-medium">{{.Name}}</span>
<svg viewBox="0 0 16 16" class="size-4 text-faint transition-transform group-hover:translate-x-0.5" fill="none" aria-hidden="true"><path d="m6 3.5 4.5 4.5L6 12.5" stroke="currentColor" stroke-width="1.4" stroke-linecap="round" stroke-linejoin="round"/></svg>
</button></form>{{end}}
</div>
<div id="none" hidden class="pt-8 text-center"><p class="text-[16px] font-medium">No banks match “<span id="query"></span>”</p><p class="mt-1.5 text-[14px] text-muted">Only banks in this setup are listed.</p></div>
<p id="blocked" hidden class="mt-4 text-[14px] text-muted" role="alert">Allow pop-ups to sign in at your bank.</p>
</div>
{{if .Overlay}}<div id="down" hidden class="flex flex-1 flex-col items-center justify-center text-center">
<span class="grid size-14 place-items-center rounded-full bg-hover text-[24px] font-semibold">!</span>
<h1 class="mt-6 text-[24px] leading-tight font-semibold"><span id="bank-down"></span> isn't responding</h1>
<p class="mt-2 text-[14px] text-muted">Try again in a moment, or pick another bank.</p>
<button type="button" id="retry" class="mt-8 h-12 w-full cursor-pointer rounded-[10px] bg-primary text-[15px] font-medium text-on-primary hover:opacity-90">Try again</button>
<button type="button" id="other" class="mt-2 h-12 w-full cursor-pointer rounded-[10px] text-[15px] font-medium text-muted hover:text-ink">Pick another bank</button>
</div>{{end}}
</main>
<script src="/assets/connect-frame.js"></script>
</body>
</html>{{end}}

{{define "pick"}}<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="color-scheme" content="dark light">
<title>{{.Title}}</title>
<link rel="stylesheet" href="{{css}}">
<link rel="icon" href="/assets/icon.svg" type="image/svg+xml">
</head>
<body class="min-h-screen">
<form method="post" action="/link/cancel"><input type="hidden" name="csrf" value="{{.CSRF}}"><button type="submit" class="absolute top-3 right-3 grid size-11 cursor-pointer place-items-center rounded-full text-muted hover:bg-hover hover:text-ink" aria-label="Close"><svg viewBox="0 0 16 16" class="size-4" fill="none" aria-hidden="true"><path d="m3.5 3.5 9 9m0-9-9 9" stroke="currentColor" stroke-width="1.4" stroke-linecap="round"/></svg></button></form>
<main class="mx-auto flex min-h-screen max-w-[420px] flex-col p-6">
<div class="flex items-center gap-3 pt-1">
<span class="grid size-10 shrink-0 place-items-center rounded-[10px] bg-hover text-[15px] font-medium">{{initial .Bank}}</span>
<span class="text-[16px] font-medium">{{.Bank}}</span>
</div>
<form method="post" action="/link/accounts" class="flex flex-1 flex-col pt-8">
<input type="hidden" name="csrf" value="{{.CSRF}}">
<h1 class="text-[24px] leading-tight font-semibold">Pick accounts</h1>
<p class="mt-2 text-[14px] text-muted">{{.PartnerName}} will see the accounts you pick.</p>
<div class="mt-6 overflow-hidden rounded-xl border border-line bg-panel">
{{range .Rows}}<label class="flex h-[68px] cursor-pointer items-center gap-3.5 border-b border-line px-4 last:border-b-0">
<span class="relative size-5 shrink-0"><input type="checkbox" name="account" value="{{.NUBAN}}" class="peer absolute inset-0 size-5 cursor-pointer appearance-none rounded-md border border-line-strong checked:border-primary checked:bg-primary focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-line-strong"{{if .Checked}} checked{{end}}>
<svg viewBox="0 0 16 16" class="pointer-events-none absolute inset-0 m-auto size-3.5 text-on-primary opacity-0 peer-checked:opacity-100" fill="none" aria-hidden="true"><path d="m3.5 8.5 3 3 6-7" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"/></svg></span>
<span class="flex flex-col"><span class="text-[16px] font-medium">{{.Name}}</span><span class="font-mono text-[13px] text-muted">•••• {{.Mask}}</span></span>
</label>{{end}}
</div>
{{template "error" .}}
<div class="mt-auto pt-8"><button type="submit" id="continue" class="h-12 w-full cursor-pointer rounded-[10px] bg-primary text-[15px] font-medium text-on-primary hover:opacity-90 disabled:cursor-not-allowed disabled:opacity-40"{{if not .Ticked}} disabled{{end}}>Continue</button></div>
</form>
</main>
<script src="/assets/connect-pick.js"></script>
</body>
</html>{{end}}

{{define "connected"}}<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="color-scheme" content="dark light">
<title>Connected</title>
<link rel="stylesheet" href="{{css}}">
<link rel="icon" href="/assets/icon.svg" type="image/svg+xml">
</head>
<body class="min-h-screen" data-result="{{.Result}}" data-parent="{{.Parent}}">
<button type="button" id="close" class="absolute top-3 right-3 grid size-11 cursor-pointer place-items-center rounded-full text-muted hover:bg-hover hover:text-ink" aria-label="Close"><svg viewBox="0 0 16 16" class="size-4" fill="none" aria-hidden="true"><path d="m3.5 3.5 9 9m0-9-9 9" stroke="currentColor" stroke-width="1.4" stroke-linecap="round"/></svg></button>
<main class="mx-auto flex min-h-screen max-w-[420px] flex-col items-center justify-center p-6 text-center">
<span class="grid size-14 place-items-center rounded-full bg-primary text-on-primary"><svg viewBox="0 0 16 16" class="size-6" fill="none" aria-hidden="true"><path d="m3.5 8.5 3 3 6-7" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"/></svg></span>
<h1 class="mt-6 text-[24px] leading-tight font-semibold">Connected</h1>
<p class="mt-2 text-[14px] text-muted">{{.Bank}} is now connected to {{.PartnerName}}.</p>
<button type="button" id="done" class="mt-8 h-12 w-full cursor-pointer rounded-[10px] bg-primary text-[15px] font-medium text-on-primary hover:opacity-90">Done</button>
</main>
<script src="/assets/connect-done.js"></script>
</body>
</html>{{end}}

{{define "relay"}}<!doctype html>
<html lang="en" data-theme="light">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.Title}}</title>
<link rel="stylesheet" href="{{css}}">
</head>
<body class="min-h-screen p-6" data-result="{{.Result}}" data-parent="{{.Parent}}">
<p class="text-[14px] text-muted">You can close this window.</p>
<script src="/assets/connect-relay.js"></script>
</body>
</html>{{end}}

{{define "message"}}{{template "head" .}}
<h1 class="text-[22px] leading-snug">{{.Heading}}</h1>
<p class="mt-2 text-[13.5px] leading-relaxed text-muted">{{.Message}}</p>
{{template "foot" .}}{{end}}
`))
