package connect

import "html/template"

const logoSVG = `<svg viewBox="0 0 24 24" aria-hidden="true" class="size-[18px]"><defs><linearGradient id="bm-top" x1="0" y1="0" x2="0" y2="1"><stop offset="0" class="mark-top-1"/><stop offset="1" class="mark-top-2"/></linearGradient><linearGradient id="bm-left" x1="0" y1="0" x2="0" y2="1"><stop offset="0" class="mark-left-1"/><stop offset="1" class="mark-left-2"/></linearGradient><linearGradient id="bm-right" x1="0" y1="0" x2="0" y2="1"><stop offset="0" class="mark-right-1"/><stop offset="1" class="mark-right-2"/></linearGradient></defs><path d="M6.09 10.68L12.00 14.09L12.00 20.92L6.09 17.50Z" fill="url(#bm-left)"/><path d="M17.91 10.68L12.00 14.09L12.00 20.92L17.91 17.50Z" fill="url(#bm-right)"/><path d="M12.00 7.26L17.91 10.68L12.00 14.09L6.09 10.68Z" fill="url(#bm-top)"/><path d="M13.83 15.15L16.11 16.46L16.11 18.92L13.83 17.61Z" fill="url(#bm-left)"/><path d="M18.24 15.23L16.11 16.46L16.11 18.92L18.24 17.69Z" fill="url(#bm-right)"/><path d="M15.96 13.92L18.24 15.23L16.11 16.46L13.83 15.15Z" fill="url(#bm-top)"/><path d="M5.76 15.23L7.89 16.46L7.89 18.92L5.76 17.69Z" fill="url(#bm-left)"/><path d="M10.17 15.15L7.89 16.46L7.89 18.92L10.17 17.61Z" fill="url(#bm-right)"/><path d="M8.04 13.92L10.17 15.15L7.89 16.46L5.76 15.23Z" fill="url(#bm-top)"/><path d="M9.87 8.12L12.00 9.35L12.00 11.97L9.87 10.75Z" fill="url(#bm-left)"/><path d="M14.13 8.12L12.00 9.35L12.00 11.97L14.13 10.75Z" fill="url(#bm-right)"/><path d="M12.00 6.89L14.13 8.12L12.00 9.35L9.87 8.12Z" fill="url(#bm-top)"/><path d="M14.99 15.81L19.24 18.27L19.24 23.19L14.99 20.73Z" fill="url(#bm-left)"/><path d="M23.50 15.81L19.24 18.27L19.24 23.19L23.50 20.73Z" fill="url(#bm-right)"/><path d="M19.24 13.36L23.50 15.81L19.24 18.27L14.99 15.81Z" fill="url(#bm-top)"/><path d="M0.50 15.81L4.76 18.27L4.76 23.19L0.50 20.73Z" fill="url(#bm-left)"/><path d="M9.01 15.81L4.76 18.27L4.76 23.19L9.01 20.73Z" fill="url(#bm-right)"/><path d="M4.76 13.36L9.01 15.81L4.76 18.27L0.50 15.81Z" fill="url(#bm-top)"/><path d="M7.74 3.27L12.00 5.73L12.00 10.64L7.74 8.19Z" fill="url(#bm-left)"/><path d="M16.26 3.27L12.00 5.73L12.00 10.64L16.26 8.19Z" fill="url(#bm-right)"/><path d="M12.00 0.81L16.26 3.27L12.00 5.73L7.74 3.27Z" fill="url(#bm-top)"/></svg>`

var pages = template.Must(template.New("").Funcs(uiFuncs).Funcs(template.FuncMap{
	"logo": func() template.HTML { return template.HTML(logoSVG) },
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

{{define "login"}}{{template "head" .}}
{{template "steps" 2}}
<h1 class="text-[22px] leading-snug">Sign in to {{.Brand}}</h1>
<p class="mt-2 text-[13.5px] leading-relaxed text-muted">Use your online banking details. {{.PartnerName}} never sees them.</p>
{{template "error" .}}
<form method="post" action="/bank/login" class="mt-6 space-y-4">
<input type="hidden" name="csrf" value="{{.CSRF}}">
<label class="block"><span class="mb-1.5 block text-[12.5px] text-muted">Username</span><input class="field" type="text" name="username" autocomplete="username" required autofocus></label>
<label class="block"><span class="mb-1.5 block text-[12.5px] text-muted">Password</span><input class="field" type="password" name="password" autocomplete="current-password" required></label>
<button type="submit" class="btn btn-primary mt-2 w-full">Continue</button>
</form>
{{template "cancel" .}}
{{template "foot" .}}{{end}}

{{define "otp"}}{{template "head" .}}
{{template "steps" 3}}
<h1 class="text-[22px] leading-snug">Verify it's you</h1>
<p class="mt-2 text-[13.5px] leading-relaxed text-muted">We sent a 6-digit code to the phone number on your account.</p>
<div class="mt-5 rounded-xl border border-line bg-raised px-3.5 py-3">
<div class="flex items-center justify-between text-[12px] text-muted"><span class="flex items-center gap-1.5"><svg viewBox="0 0 16 16" class="size-3.5" fill="none" aria-hidden="true"><path d="M2.5 4.5h11v7h-6l-3 2.5v-2.5h-2v-7Z" stroke="currentColor" stroke-width="1.2" stroke-linejoin="round"/></svg>Messages · {{.Brand}}</span><span>now</span></div>
<p class="mt-1.5 text-[13.5px] leading-relaxed">Your {{.Brand}} verification code is <span class="num text-ink">{{.OTP}}</span>. It expires in 5 minutes. Never share this code with anyone.</p>
</div>
{{template "error" .}}
<form method="post" action="/bank/otp" class="mt-5 space-y-4">
<input type="hidden" name="csrf" value="{{.CSRF}}">
<label class="block"><span class="mb-1.5 block text-[12.5px] text-muted">Verification code</span><input class="field h-13 text-center font-mono text-[20px] tracking-[0.4em]" type="text" name="otp" inputmode="numeric" autocomplete="one-time-code" maxlength="6" required autofocus></label>
<button type="submit" class="btn btn-primary w-full">Verify</button>
</form>
{{template "cancel" .}}
{{template "foot" .}}{{end}}

{{define "consent"}}{{template "head" .}}
{{template "steps" 4}}
<h1 class="text-[22px] leading-snug">Share account data with {{.PartnerName}}</h1>
<p class="mt-2 text-[13.5px] leading-relaxed text-muted">{{.ConnectBrand}}, on behalf of {{.PartnerName}}, is asking for access {{.Purpose}}.</p>
<dl class="mt-6 grid grid-cols-[124px_1fr] gap-x-4 gap-y-2.5 text-[13px]">
<dt class="text-muted">Receiving party</dt><dd>{{.ConnectLegal}}</dd>
<dt class="text-muted">Registry number</dt><dd class="num">{{.RegistryID}}</dd>
<dt class="text-muted">CAC number</dt><dd class="num">{{.CAC}}</dd>
<dt class="text-muted">Acting for</dt><dd>{{.PartnerLegal}} ({{.PartnerName}})</dd>
<dt class="text-muted">Access</dt><dd>Account details, balances and transactions. Read only.</dd>
<dt class="text-muted">History from</dt><dd>{{.Start}}{{if .Effective}} <span class="text-muted">· {{.Brand}} keeps history from {{.Effective}}</span>{{end}}</dd>
<dt class="text-muted">Duration</dt><dd>12 months, until {{.Until}}</dd>
<dt class="text-muted">Frequency</dt><dd>Several times a day, and when {{.PartnerName}} asks</dd>
</dl>
<div class="my-6 h-px bg-line"></div>
{{template "error" .}}
<form method="post" action="/bank/consent">
<input type="hidden" name="csrf" value="{{.CSRF}}">
<p class="mb-2.5 text-[12.5px] text-muted">Accounts to share</p>
<div class="space-y-2">
{{range .Accounts}}<label class="flex cursor-pointer items-start gap-3 rounded-xl border border-line px-3.5 py-3 transition-colors hover:border-line-strong has-[:checked]:border-line-strong has-[:checked]:bg-raised">
<input type="checkbox" name="account" value="{{.Key}}" class="mt-1 accent-[var(--primary)]" {{if .Checked}}checked{{end}}>
<span class="flex-1"><span class="block text-[14px]">{{.Name}}</span><span class="block text-[12.5px] text-muted"><span class="capitalize">{{.Type}}</span> · <span class="num">{{.NUBAN}}</span></span></span>
</label>{{end}}
<label class="flex cursor-pointer items-start gap-3 rounded-xl border border-line px-3.5 py-3 transition-colors hover:border-line-strong">
<input type="checkbox" name="analytics" value="yes" class="mt-1 accent-[var(--primary)]">
<span class="flex-1"><span class="block text-[14px]">Anonymised analytics</span><span class="block text-[12.5px] text-muted">Optional. You can connect without it.</span></span>
</label>
</div>
<p class="mt-5 text-[12.5px] leading-relaxed text-muted">You can withdraw access at any time from {{.Brand}}'s connected apps or from {{.ConnectBrand}}'s customer page. When you do, {{.ConnectBrand}} stops collecting data and deletes what it holds within 30 days. {{.PartnerName}} keeps records already in your books under its own privacy notice.</p>
<div class="mt-6 grid grid-cols-2 gap-2">
<button type="submit" name="action" value="decline" class="btn btn-secondary w-full">Decline</button>
<button type="submit" name="action" value="approve" class="btn btn-primary w-full">Approve</button>
</div>
</form>
{{template "foot" .}}{{end}}

{{define "message"}}{{template "head" .}}
<h1 class="text-[22px] leading-snug">{{.Heading}}</h1>
<p class="mt-2 text-[13.5px] leading-relaxed text-muted">{{.Message}}</p>
{{template "foot" .}}{{end}}

{{define "customer_login"}}{{template "head" .}}
<h1 class="text-[22px] leading-snug">Your connected apps</h1>
<p class="mt-2 text-[13.5px] leading-relaxed text-muted">Sign in with your online banking details to see the apps that read your accounts through {{.ConnectBrand}}, and to withdraw access.</p>
{{template "error" .}}
<form method="post" action="/customer/login" class="mt-6 space-y-4">
<label class="block"><span class="mb-1.5 block text-[12.5px] text-muted">Username</span><input class="field" type="text" name="username" autocomplete="username" required autofocus></label>
<label class="block"><span class="mb-1.5 block text-[12.5px] text-muted">Password</span><input class="field" type="password" name="password" autocomplete="current-password" required></label>
<button type="submit" class="btn btn-primary mt-2 w-full">Sign in</button>
</form>
{{template "foot" .}}{{end}}

{{define "customer"}}{{template "head" .}}
<h1 class="text-[22px] leading-snug">Your connected apps</h1>
<p class="mt-2 text-[13.5px] leading-relaxed text-muted">These apps read your accounts through {{.ConnectBrand}}.</p>
{{if not .Connections}}<p class="mt-6 rounded-xl border border-line px-4 py-8 text-center text-[13.5px] text-muted">No apps are connected.</p>{{end}}
<ul class="mt-6 space-y-2">
{{range .Connections}}<li class="flex items-center gap-3 rounded-xl border border-line px-3.5 py-3">
<span class="flex-1"><span class="block text-[14px]">{{.Partner}}</span><span class="block text-[12.5px] text-muted">{{.Bank}} · since {{.Since}}</span></span>
{{if .Active}}<form method="post" action="/customer/revoke"><input type="hidden" name="csrf" value="{{$.CSRF}}"><input type="hidden" name="connection" value="{{.ID}}"><button type="submit" class="btn btn-secondary btn-sm">Withdraw access</button></form>{{else}}<span class="badge badge-bad">{{human .Status}}</span>{{end}}
</li>{{end}}
</ul>
{{template "foot" .}}{{end}}
`))
