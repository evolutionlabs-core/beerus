package aggregator

import (
	"html/template"

	"github.com/evolutionlabs-core/beerus/internal/view"
)

var pages = template.Must(template.New("").Funcs(view.Funcs()).Parse(`
{{define "doc"}}<!doctype html>
<html lang="en" data-theme="light">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.Title}}</title>
<link rel="stylesheet" href="{{css}}">
<link rel="icon" href="/assets/icon.svg" type="image/svg+xml">
</head>{{end}}

{{define "x"}}<svg viewBox="0 0 24 24" class="size-[16px]" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" aria-hidden="true"><path d="M6 6l12 12M18 6L6 18"/></svg>{{end}}

{{define "cancel"}}<form method="post" action="/link/cancel" class="dialog-close"><input type="hidden" name="csrf" value="{{.CSRF}}"><button type="submit" id="close" class="dialog-icon" aria-label="Close">{{template "x"}}</button></form>{{end}}

{{define "foot"}}<footer class="dialog-foot"><svg viewBox="0 0 24 24" class="size-[14px]" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><rect x="5" y="11" width="14" height="9" rx="2"/><path d="M8 11V8a4 4 0 0 1 8 0v3"/></svg>Connected securely by {{.ConnectBrand}}</footer>{{end}}

{{define "error"}}{{if .Error}}<p class="dialog-error" role="alert">{{.Error}}</p>{{end}}{{end}}

{{define "choose"}}{{template "doc" .}}
<body class="bg-white" data-overlay="{{.Overlay}}" data-fixed="{{.Fixed}}" data-parent="{{.Parent}}" data-app="{{.PartnerName}}">
<main class="dialog">
{{template "cancel" .}}
<div id="list" class="flex min-h-0 flex-1 flex-col">
<div class="dialog-bar pl-[20px]"><h1 class="text-[16px] font-medium tracking-normal">Link a bank</h1></div>
<div class="dialog-body">
<label class="flex h-[44px] shrink-0 items-center gap-[8px] rounded-[6px] border border-neutral-300 px-[12px] text-neutral-500 focus-within:border-accent focus-within:shadow-[0_0_0_1px_var(--color-accent)]">
<svg viewBox="0 0 24 24" class="size-[16px] shrink-0" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" aria-hidden="true"><circle cx="11" cy="11" r="7"/><path d="M20 20l-3.5-3.5"/></svg>
<input id="search" type="search" autocomplete="off" placeholder="Search banks" aria-label="Search banks" class="min-w-0 flex-1 bg-transparent text-[14px] text-neutral-950 outline-none placeholder:text-neutral-500">
</label>
<div class="flex flex-col [&>form:last-of-type>button]:border-b-0">
{{range .Institutions}}<form method="post" action="/link/institution"{{if $.Overlay}} target="beerus_bank"{{end}} data-bank data-id="{{.ID}}" data-name="{{.Name}}" data-initials="{{initials .Name}}">
<input type="hidden" name="csrf" value="{{$.CSRF}}"><input type="hidden" name="institution" value="{{.ID}}">
<button type="submit" class="group flex min-h-[60px] w-full cursor-pointer items-center gap-[12px] border-b border-[#f0f0f0] text-left focus-visible:outline-2 focus-visible:outline-accent">
<span class="grid size-[36px] shrink-0 place-items-center rounded-[8px] bg-neutral-100 text-[13px] font-semibold text-neutral-700">{{initials .Name}}</span>
<span class="flex-1 text-[14px] font-medium">{{.Name}}</span>
<svg viewBox="0 0 24 24" class="size-[16px] text-neutral-400 transition-transform group-hover:translate-x-0.5" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M9 6l6 6-6 6"/></svg>
</button></form>{{end}}
</div>
<div id="none" hidden class="flex flex-col items-center gap-[4px] pt-[32px] text-center"><p class="text-[14px] font-medium text-neutral-900">No banks match “<span id="query"></span>”</p><p class="dialog-lede">Only banks in this setup are listed.</p></div>
<p id="blocked" hidden class="dialog-error" role="alert">Allow pop-ups to sign in at your bank.</p>
</div>
</div>
{{if .Overlay}}<div id="down" hidden class="flex min-h-0 flex-1 flex-col">
<div class="dialog-bar pl-[8px]">
<button type="button" id="back" class="dialog-icon" aria-label="Back to banks"><svg viewBox="0 0 24 24" class="size-[16px]" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M15 6l-6 6 6 6"/></svg></button>
<span class="dialog-tile" data-down-initials></span><span class="dialog-name" data-down-name></span>
</div>
<div class="dialog-body gap-[12px]">
<div class="flex flex-1 flex-col items-center justify-center gap-[16px] text-center">
<span class="grid size-[48px] place-items-center rounded-full bg-neutral-100 text-neutral-700"><svg viewBox="0 0 24 24" class="size-[22px]" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" aria-hidden="true"><path d="M12 7v6M12 17h.01"/></svg></span>
<div class="flex flex-col gap-[4px]"><h2 class="dialog-title"><span data-down-name></span> isn’t responding</h2><p class="dialog-lede">Try again in a moment, or pick another bank.</p></div>
</div>
<button type="button" id="retry" class="dialog-primary">Try again</button>
<button type="button" id="other" class="dialog-secondary">Pick another bank</button>
</div>
</div>{{end}}
{{template "foot" .}}
</main>
<script src="/assets/connect-frame.js"></script>
</body>
</html>{{end}}

{{define "pick"}}{{template "doc" .}}
<body class="bg-white">
<main class="dialog">
{{template "cancel" .}}
<div class="dialog-bar pl-[20px]"><span class="dialog-tile">{{initials .Bank}}</span><span class="dialog-name">{{.Bank}}</span></div>
<form method="post" action="/link/accounts" class="dialog-body">
<input type="hidden" name="csrf" value="{{.CSRF}}">
<div class="flex flex-col gap-[4px]"><h1 class="dialog-title">Pick accounts</h1><p class="dialog-lede">{{.PartnerName}} will see the accounts you pick.</p></div>
<div class="flex flex-col overflow-hidden rounded-[8px] border border-neutral-200">
{{range .Rows}}<label class="flex min-h-[64px] cursor-pointer items-center gap-[12px] border-b border-neutral-200 px-[16px] last:border-b-0">
<input type="checkbox" name="account" value="{{.NUBAN}}" class="size-[18px] shrink-0 cursor-pointer accent-accent"{{if .Checked}} checked{{end}}>
<span class="flex flex-col gap-[2px]"><span class="text-[14px] font-medium text-neutral-900">{{.Name}}</span><span class="font-mono text-[12px] text-neutral-500">•••• {{.Mask}}</span></span>
</label>{{end}}
</div>
{{template "error" .}}
<button type="submit" id="continue" class="dialog-primary mt-[4px]"{{if not .Ticked}} disabled{{end}}>Continue</button>
</form>
{{template "foot" .}}
</main>
<script src="/assets/connect-pick.js"></script>
</body>
</html>{{end}}

{{define "connected"}}{{template "doc" .}}
<body class="bg-white" data-result="{{.Result}}" data-parent="{{.Parent}}">
<main class="dialog">
<div class="dialog-close"><button type="button" id="close" class="dialog-icon" aria-label="Close">{{template "x"}}</button></div>
<div class="dialog-bar pl-[20px]"><span class="dialog-tile">{{initials .Bank}}</span><span class="dialog-name">{{.Bank}}</span></div>
<div class="dialog-body">
<div class="flex flex-1 flex-col items-center justify-center gap-[16px] text-center">
<span class="grid size-[48px] place-items-center rounded-full bg-accent text-white"><svg viewBox="0 0 24 24" class="size-[22px]" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M5 12.5l4.5 4.5L19 7.5"/></svg></span>
<div class="flex flex-col gap-[4px]"><h1 class="dialog-title">Connected</h1><p class="dialog-lede">{{.Bank}} is now connected to {{.PartnerName}}.</p></div>
</div>
<button type="button" id="done" class="dialog-primary">Done</button>
</div>
{{template "foot" .}}
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

{{define "message"}}{{template "doc" .}}
<body class="bg-white">
<main class="dialog">
<div class="dialog-bar pl-[20px]"><span class="text-[16px] font-medium">{{.Brand}}</span></div>
<div class="dialog-body"><div class="flex flex-col gap-[4px]"><h1 class="dialog-title">{{.Heading}}</h1><p class="dialog-lede">{{.Message}}</p></div></div>
{{template "foot" .}}
</main>
</body>
</html>{{end}}
`))
