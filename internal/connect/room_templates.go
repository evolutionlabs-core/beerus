package connect

const roomTemplates = `
{{define "room_head"}}<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="color-scheme" content="dark light">
<title>{{.Title}} · Beerus</title>
{{if .Refresh}}<meta http-equiv="refresh" content="2">{{end}}
<link rel="stylesheet" href="{{css}}">
<script src="/assets/theme.js"></script>
<link rel="icon" href="/assets/icon.svg" type="image/svg+xml">
</head>
<body class="min-h-screen">
<div class="grid min-h-screen grid-cols-1 content-start lg:grid-cols-[236px_1fr]">
<aside class="flex items-center gap-1 overflow-x-auto border-b border-line px-3 py-2.5 lg:sticky lg:top-0 lg:block lg:h-screen lg:overflow-y-auto lg:border-r lg:border-b-0 lg:px-3.5 lg:py-5">
<a href="/ui" class="mr-3 flex shrink-0 items-center gap-2.5 px-2 text-[14px] tracking-tight lg:mr-0 lg:pb-5">{{logo}}<span>Beerus</span></a>
<nav class="flex shrink-0 gap-0.5 text-[13.5px] lg:block lg:space-y-0.5">
<a href="/ui" class="flex h-8 shrink-0 items-center rounded-lg px-2 whitespace-nowrap {{if eq .Nav "overview"}}bg-raised text-ink{{else}}text-muted hover:bg-raised hover:text-ink{{end}}">Overview</a>
<a href="/ui/transfer" class="flex h-8 shrink-0 items-center rounded-lg px-2 whitespace-nowrap {{if eq .Nav "transfer"}}bg-raised text-ink{{else}}text-muted hover:bg-raised hover:text-ink{{end}}">New transfer</a>
<a href="/ui/banks" class="flex h-8 shrink-0 items-center rounded-lg px-2 whitespace-nowrap {{if eq .Nav "banks"}}bg-raised text-ink{{else}}text-muted hover:bg-raised hover:text-ink{{end}}">Banks</a>
</nav>
<p class="mt-6 hidden px-2 pb-1.5 text-[11.5px] text-faint lg:block">Beerus Connect</p>
<nav class="flex shrink-0 gap-0.5 text-[13.5px] lg:block lg:space-y-0.5">
<a href="/ui/connections" class="flex h-8 shrink-0 items-center rounded-lg px-2 whitespace-nowrap {{if eq .Nav "connections"}}bg-raised text-ink{{else}}text-muted hover:bg-raised hover:text-ink{{end}}">Connections</a>
<a href="/ui/webhooks" class="flex h-8 shrink-0 items-center rounded-lg px-2 whitespace-nowrap {{if eq .Nav "webhooks"}}bg-raised text-ink{{else}}text-muted hover:bg-raised hover:text-ink{{end}}">Webhooks</a>
</nav>
</aside>
<div class="min-w-0">
<header class="sticky top-0 z-10 flex h-14 items-center justify-between gap-4 border-b border-line bg-canvas/85 px-5 backdrop-blur-md lg:px-8">
<div class="truncate text-[13.5px] text-muted">{{block "crumbs" .}}{{.Title}}{{end}}</div>
<div class="flex items-center gap-2.5">
<span class="hidden text-muted sm:inline text-[12.5px]">{{when .Now}} WAT</span>
{{themeToggle}}
</div>
</header>
<main class="max-w-[1180px] px-5 py-8 lg:px-8">
{{end}}

{{define "room_foot"}}</main>
</div>
</div>
</body>
</html>{{end}}

{{define "stat"}}<div class="rounded-2xl border border-line bg-panel px-5 py-4"><p class="text-[12.5px] text-muted">{{index . 0}}</p><p class="mt-2 text-[22px] tracking-tight tabular-nums">{{index . 1}}</p>{{if index . 2}}<p class="mt-1 text-[12px] text-faint">{{index . 2}}</p>{{end}}</div>{{end}}

{{define "overview"}}{{template "room_head" .}}
<div class="mb-7"><h1 class="text-[24px]">Overview</h1><p class="mt-1.5 text-[13.5px] text-muted">Business time {{when .Now}} WAT</p></div>
<div class="grid grid-cols-2 gap-3 lg:grid-cols-4">
{{template "stat" (list "Customer deposits" (naira .Total) "Across all banks")}}
{{range .BankSums}}{{template "stat" (list .Name (naira .Deposits) (count .Accounts "account" "accounts"))}}{{end}}
{{template "stat" (list "Connections" (printf "%d active" .Active) (printf "%d in total" .Connections))}}
</div>
<div class="mt-8"><a href="/ui/transfer" class="btn btn-primary btn-sm">New transfer</a></div>
{{template "room_foot" .}}{{end}}

{{define "bank"}}{{template "room_head" .}}
<div class="{{.Bank.ID}} mb-7 flex items-center gap-4">
<span class="grid size-11 place-items-center rounded-xl border border-line-strong bg-raised text-[17px] text-bank">{{initial .Bank.Name}}</span>
<div><h1 class="text-[24px]">{{.Bank.Name}}</h1><p class="mt-1 text-[13.5px] text-muted">{{if eq .Bank.Type "digital"}}Digital bank{{else if eq .Bank.Type "microfinance"}}Microfinance bank{{else}}Commercial bank{{end}} · NIP code <span class="num">{{.Bank.NIPCode}}</span> · history kept {{count .Bank.HistoryDays "day" "days"}}</p></div>
<div class="ml-auto flex items-center gap-2.5">
{{if eq .Status.Phase "closed"}}<span class="badge">Closed</span>{{else if eq .Status.Phase "suspended"}}<span class="badge badge-warn">Suspended</span>{{else if .Down}}<span class="badge badge-bad">Unavailable</span>{{else}}<span class="badge badge-ok">Operational</span>{{end}}
{{if and .Managed (ne .Status.Phase "closed")}}{{if eq .Status.Phase "suspended"}}<form method="post" action="/ui/banks/{{.Bank.ID}}/resume"><button type="submit" class="btn btn-secondary btn-sm">Resume</button></form>{{else}}<form method="post" action="/ui/banks/{{.Bank.ID}}/suspend"><button type="submit" class="btn btn-secondary btn-sm">Suspend</button></form>{{end}}<a href="/ui/banks/{{.Bank.ID}}/close" class="btn btn-danger btn-sm">Close bank</a>{{end}}
{{if and .Managed (eq .Status.Phase "closed")}}<a href="/ui/banks/{{.Bank.ID}}/purge" class="btn btn-danger btn-sm">Purge bank</a>{{end}}
</div>
</div>
{{if eq .Status.Phase "closed"}}<p class="mb-6 rounded-[10px] border border-line bg-panel px-4 py-3 text-[13px] text-muted">This bank is closed. Its online banking is off, its Connect consents were revoked, and nothing moves in its accounts again. Any balance left here is either money nobody claimed or the uninsured part of a deposit, which the customer can only recover from the bank's liquidation.</p>{{end}}
{{if eq .Status.Phase "suspended"}}<p class="mb-6 rounded-[10px] border border-line bg-panel px-4 py-3 text-[13px] text-muted">The CBN has suspended this bank. Its accounts are frozen: transfers in and out are rejected, nothing new is posted, and connections report the bank as down until you resume it.</p>{{end}}
<div class="grid grid-cols-2 gap-3 lg:grid-cols-3">
{{template "stat" (list "Customer deposits" (naira .Totals.Deposits) "")}}
{{template "stat" (list "Customers" (printf "%d" .Totals.Customers) "")}}
{{template "stat" (list "Accounts" (printf "%d" .Totals.Accounts) "")}}
</div>
<section class="mt-8 rounded-2xl border border-line bg-panel">
<div class="border-b border-line px-5 py-3.5"><h2 class="text-[15px]">Accounts</h2></div>
<div class="overflow-x-auto"><table class="w-full min-w-[640px] border-collapse text-[13.5px]">
<thead><tr class="text-left text-[12px] text-faint"><th class="border-b border-line px-5 py-2.5 font-normal">Holder</th><th class="border-b border-line px-5 py-2.5 font-normal">Account</th><th class="border-b border-line px-5 py-2.5 font-normal">Type</th><th class="border-b border-line px-5 py-2.5 text-right font-normal">Available</th><th class="border-b border-line px-5 py-2.5 text-right font-normal">Ledger balance</th></tr></thead>
<tbody>{{range .Accounts}}<tr class="group hover:bg-raised">
<td class="border-b border-line px-5 py-3 group-last:border-b-0"><a href="/ui/accounts/{{pathq .Key}}" class="block"><span class="block">{{.Holder}}</span><span class="block text-[12.5px] text-muted">{{.Name}}</span></a></td>
<td class="border-b border-line px-5 py-3 group-last:border-b-0"><a href="/ui/accounts/{{pathq .Key}}" class="num">{{.NUBAN}}</a></td>
<td class="border-b border-line px-5 py-3 capitalize text-muted group-last:border-b-0">{{.Type}}</td>
<td class="border-b border-line px-5 py-3 text-right group-last:border-b-0"><span class="num">{{naira .Available}}</span></td>
<td class="border-b border-line px-5 py-3 text-right group-last:border-b-0"><span class="num text-muted">{{naira .Ledger}}</span></td>
</tr>{{end}}</tbody></table></div>
{{if not .Accounts}}<p class="px-5 py-10 text-center text-muted">No accounts yet. Create a customer and open an account through the control API.</p>{{end}}
{{template "pager" .}}
</section>
{{template "room_foot" .}}{{end}}

{{define "pager"}}{{if or .Next .First}}<div class="flex items-center justify-between border-t border-line px-5 py-3 text-[13px]">
{{if .First}}<a href="?" class="btn btn-secondary btn-sm">First page</a>{{else}}<span></span>{{end}}
{{if .Next}}<a href="?cursor={{.Next}}" class="btn btn-secondary btn-sm">Next page</a>{{else}}<span></span>{{end}}
</div>{{end}}{{end}}

{{define "room_failed"}}{{template "room_head" .}}
<h1 class="text-[24px]">Something went wrong</h1><p class="mt-1.5 text-[13.5px] break-words text-muted">{{.Error}}</p>
{{template "room_foot" .}}{{end}}

{{define "account"}}{{template "room_head" .}}
{{with .Transfer}}<p class="mb-6 rounded-xl border border-pos/30 bg-pos/10 px-4 py-3 text-[13.5px] text-pos">{{.}}</p>{{end}}
<div class="{{.A.Bank.ID}} mb-7 flex flex-wrap items-center gap-4">
<span class="grid size-11 place-items-center rounded-xl border border-line-strong bg-raised text-[17px] text-bank">{{initial .A.Bank.Name}}</span>
<div class="min-w-0"><h1 class="text-[24px]">{{.A.Holder}}</h1><p class="mt-1 text-[13.5px] text-muted"><a href="/ui/banks/{{.A.Bank.ID}}" class="hover:text-ink">{{.A.Bank.Name}}</a> · <span class="capitalize">{{.A.Type}}</span> · <span class="num">{{.A.NUBAN}}</span> · {{.A.Name}}</p></div>
<a href="/ui/transfer?from={{.A.Key}}" class="btn btn-primary ml-auto">Send money</a>
</div>
<div class="grid grid-cols-2 gap-3 lg:grid-cols-4">
{{template "stat" (list "Available balance" (naira .A.Available) "")}}
{{template "stat" (list "Ledger balance" (naira .A.Ledger) "")}}
</div>
{{if .Shares}}<section class="mt-8 rounded-2xl border border-line bg-panel">
<div class="border-b border-line px-5 py-3.5"><h2 class="text-[15px]">Shared through Beerus Connect</h2></div>
<ul>{{range .Shares}}<li class="flex items-center gap-4 border-b border-line px-5 py-3 text-[13.5px] last:border-b-0"><span class="flex-1">{{.Partner}}</span><span class="num text-muted">{{.ID}}</span><span class="text-muted">synced {{when .AsAt}}</span><span class="badge badge-{{statusClass .Status}}">{{human .Status}}</span></li>{{end}}</ul>
</section>{{end}}
<section class="mt-8 rounded-2xl border border-line bg-panel">
<div class="flex items-center justify-between border-b border-line px-5 py-3.5"><h2 class="text-[15px]">Statement</h2><span class="text-[12.5px] text-muted">Newest first</span></div>
<div class="overflow-x-auto"><table class="w-full min-w-[640px] border-collapse text-[13.5px]">
<thead><tr class="text-left text-[12px] text-faint"><th class="border-b border-line px-5 py-2.5 font-normal">Date</th><th class="border-b border-line px-5 py-2.5 font-normal">Description</th><th class="border-b border-line px-5 py-2.5 font-normal">Channel</th><th class="border-b border-line px-5 py-2.5 text-right font-normal">Amount</th><th class="border-b border-line px-5 py-2.5 text-right font-normal">Balance</th></tr></thead>
<tbody>{{range .Lines}}<tr class="group hover:bg-raised">
<td class="border-b border-line px-5 py-2.5 whitespace-nowrap text-muted group-last:border-b-0"><a href="{{.Href}}" class="block">{{when .BookedAt}}</a></td>
<td class="border-b border-line px-5 py-2.5 group-last:border-b-0"><a href="{{.Href}}" class="flex items-center gap-2"><span class="max-w-[420px] truncate">{{.Description}}</span></a></td>
<td class="border-b border-line px-5 py-2.5 whitespace-nowrap text-muted group-last:border-b-0">{{human .Channel}}</td>
<td class="border-b border-line px-5 py-2.5 text-right whitespace-nowrap group-last:border-b-0"><span class="num {{amtClass .Amount}}">{{signed .Amount}}</span></td>
<td class="border-b border-line px-5 py-2.5 text-right whitespace-nowrap group-last:border-b-0"><span class="num text-muted">{{naira .BalanceAfter}}</span></td>
</tr>{{else}}<tr><td colspan="5" class="px-5 py-10 text-center text-muted">No transactions yet.</td></tr>{{end}}</tbody></table></div>
{{template "pager" .}}
</section>
{{template "room_foot" .}}{{end}}

{{define "line"}}{{template "room_head" .}}
<a href="/ui/accounts/{{pathq .A.Key}}" class="text-[13px] text-muted hover:text-ink">← {{.A.Holder}} · {{.A.Bank.Name}} {{.A.NUBAN}}</a>
<div class="mt-4 mb-7 flex flex-wrap items-end justify-between gap-4">
<div><h1 class="text-[24px]">{{.L.Description}}</h1><p class="mt-1.5 text-[13.5px] text-muted">{{when .L.BookedAt}} WAT</p></div>
<div class="text-right"><p class="num text-[30px] tracking-tight {{amtClass .L.Amount}}">{{signed .L.Amount}}</p><span class="badge badge-ok">Posted</span></div>
</div>
<div class="grid gap-3 lg:grid-cols-[1.2fr_1fr]">
<section class="rounded-2xl border border-line bg-panel">
<div class="border-b border-line px-5 py-3.5"><h2 class="text-[15px]">Details</h2></div>
<dl class="grid grid-cols-[150px_1fr] gap-x-4 gap-y-3 px-5 py-4 text-[13.5px]">
<dt class="text-muted">Narration</dt><dd class="num break-all">{{.L.Narration}}</dd>
<dt class="text-muted">Booked</dt><dd>{{when .L.BookedAt}}</dd>
<dt class="text-muted">Value date</dt><dd>{{.L.ValueDate}}</dd>
<dt class="text-muted">Channel</dt><dd>{{human .L.Channel}}</dd>
<dt class="text-muted">Bank reference</dt><dd class="num">{{if .L.BankRef}}{{.L.BankRef}}{{else}}—{{end}}</dd>
<dt class="text-muted">NIP session ID</dt><dd class="num break-all">{{if .L.SessionID}}{{.L.SessionID}}{{else}}—{{end}}</dd>
{{with .L.Counterparty}}<dt class="text-muted">Counterparty</dt><dd>{{.Name}}{{if .AccountNumber}}<span class="block text-[12.5px] text-muted">{{.BankName}} · <span class="num">{{.AccountNumber}}</span></span>{{end}}</dd>{{end}}
<dt class="text-muted">Balance after</dt><dd><span class="num">{{naira .L.BalanceAfter}}</span></dd>
</dl>
</section>
<section class="rounded-2xl border border-line bg-panel">
<div class="border-b border-line px-5 py-3.5"><h2 class="text-[15px]">Beerus Connect</h2></div>
{{if .Shares}}<ul>{{range .Shares}}<li class="border-b border-line px-5 py-3.5 text-[13.5px] last:border-b-0">
<div class="flex items-center justify-between gap-3"><span>{{.Partner}}</span>{{if .Synced}}<span class="badge badge-ok">Synced</span>{{else}}<span class="badge">Not yet synced</span>{{end}}</div>
<p class="num mt-1.5 text-[12.5px] text-muted">{{.TxnID}}</p>
<p class="mt-0.5 text-[12.5px] text-faint">Account {{.AccountID}} · connection {{human .Status}}</p>
</li>{{end}}</ul>{{else}}<p class="px-5 py-8 text-[13.5px] text-muted">This account is not shared with any app.</p>{{end}}
</section>
</div>
{{if .Trace}}<section class="mt-3 rounded-2xl border border-line bg-panel">
<div class="flex flex-wrap items-center justify-between gap-3 border-b border-line px-5 py-3.5"><h2 class="text-[15px]">Transfer</h2>
<p class="text-[12.5px] text-muted">Debited <span class="num text-ink">{{naira .Debit}}</span> · charges <span class="num text-ink">{{naira .Charges}}</span> · credited <span class="num text-ink">{{naira .Credit}}</span></p></div>
<ol class="relative px-5 py-5 before:absolute before:top-7 before:bottom-7 before:left-[27px] before:w-px before:bg-line-strong">
{{range .Trace}}<li class="relative flex items-start gap-4 pb-4 last:pb-0">
<span class="{{.Bank.ID}} relative z-[1] mt-0.5 grid size-4 place-items-center rounded-full border border-line-strong bg-panel"><span class="size-1.5 rounded-full {{if gt .Amount 0}}bg-pos{{else}}bg-bank{{end}}"></span></span>
<a href="{{.Href}}" class="flex flex-1 flex-wrap items-baseline justify-between gap-x-4 gap-y-0.5 text-[13.5px]">
<span>{{.Description}}<span class="block text-[12.5px] text-muted">{{.Bank.Name}} · <span class="num">{{.NUBAN}}</span> · {{human .Channel}}</span></span>
<span class="num {{amtClass .Amount}}">{{signed .Amount}}</span>
</a></li>{{end}}
</ol>
</section>{{end}}
{{template "room_foot" .}}{{end}}

{{define "banks"}}{{template "room_head" .}}
<div class="mb-7 flex flex-wrap items-end justify-between gap-4"><div><h1 class="text-[24px]">Banks</h1><p class="mt-1.5 text-[13.5px] text-muted">Every bank in this setup, with its own ledger and online banking.</p></div>{{if .CanCreate}}<a href="/ui/banks/new" class="btn btn-primary btn-sm">New bank</a>{{end}}</div>
{{range .Warnings}}<p class="mb-3 rounded-[10px] border border-warn/30 bg-warn/10 px-4 py-3 text-[13px] text-warn" role="alert">{{.}}</p>{{end}}
<nav class="mb-4 flex flex-wrap gap-1.5" aria-label="Filter banks by state">{{range .Filters}}<a href="/ui/banks{{if .State}}?state={{.State}}{{end}}" class="flex h-8 items-center gap-2 rounded-lg border px-3 text-[13px] {{if .On}}border-line-strong bg-raised text-ink{{else}}border-transparent text-muted hover:bg-raised hover:text-ink{{end}}" {{if .On}}aria-current="page"{{end}}>{{.Label}}<span class="num text-faint">{{.Count}}</span></a>{{end}}</nav>
<section class="rounded-2xl border border-line bg-panel">
<div class="overflow-x-auto"><table class="w-full min-w-[760px] border-collapse text-[13.5px]">
<thead><tr class="text-left text-[12px] whitespace-nowrap text-faint"><th class="border-b border-line px-5 py-2.5 font-normal">Bank</th><th class="border-b border-line px-5 py-2.5 font-normal">NIP code</th><th class="border-b border-line px-5 py-2.5 font-normal">State</th><th class="border-b border-line px-5 py-2.5 text-right font-normal">Customers</th><th class="border-b border-line px-5 py-2.5 text-right font-normal">Deposits</th><th class="border-b border-line px-5 py-2.5 font-normal">Online banking</th></tr></thead>
<tbody>{{range .Rows}}<tr class="{{.ID}} group hover:bg-raised">
<td class="border-b border-line px-5 py-3 group-last:border-b-0"><a href="/ui/banks/{{.ID}}" class="flex items-center gap-3"><span class="grid size-7 place-items-center rounded-lg border border-line-strong bg-raised text-[12px] text-bank">{{initial .Name}}</span><span><span class="block">{{.Name}}</span><span class="block text-[12.5px] text-muted">{{if eq .Type "digital"}}Digital{{else if eq .Type "microfinance"}}Microfinance{{else}}Commercial{{end}}</span></span></a></td>
<td class="border-b border-line px-5 py-3 group-last:border-b-0"><span class="num text-muted">{{if .NIPCode}}{{.NIPCode}}{{else}}—{{end}}</span></td>
<td class="border-b border-line px-5 py-3 group-last:border-b-0">{{if eq .Phase "provisioning"}}<span class="badge badge-warn">Provisioning</span>{{else if eq .Phase "repairing"}}<span class="badge badge-bad">Repairing</span>{{else if eq .Phase "suspended"}}<span class="badge badge-warn">Suspended</span>{{else if eq .Phase "closed"}}<span class="badge">Closed</span>{{else}}<span class="badge badge-ok">Active</span>{{end}}</td>
<td class="border-b border-line px-5 py-3 text-right group-last:border-b-0"><span class="num">{{.Customers}}</span></td>
<td class="border-b border-line px-5 py-3 text-right group-last:border-b-0"><span class="num">{{naira .Deposits}}</span></td>
<td class="border-b border-line px-5 py-3 group-last:border-b-0">{{if .URL}}<a href="{{.URL}}" target="_blank" rel="noopener" class="whitespace-nowrap text-muted hover:text-ink">{{trimScheme .URL}}</a>{{else}}<span class="text-faint">—</span>{{end}}</td>
</tr>{{end}}</tbody></table></div>
{{if not .Rows}}<p class="px-5 py-10 text-center text-muted">No {{.State}} banks.</p>{{end}}
</section>
{{template "room_foot" .}}{{end}}

{{define "bank_provisioning"}}{{template "room_head" .}}
<div class="mb-7"><h1 class="text-[24px]">{{.Status.Name}}</h1><p class="mt-1.5 text-[13.5px] text-muted">{{if eq .Status.Phase "repairing"}}Repairing the bank because {{.Status.Reason}}.{{else}}Setting up the bank.{{end}} Failed steps are retried automatically.</p></div>
<ol class="max-w-[640px] rounded-2xl border border-line bg-panel">
{{range .Status.Steps}}<li class="flex items-start gap-3 border-b border-line px-5 py-3.5 text-[13.5px] last:border-b-0">
{{if .Done}}<span class="badge badge-ok">Done</span>{{else if .Error}}<span class="badge badge-bad">Retrying</span>{{else}}<span class="badge">Waiting</span>{{end}}
<div class="min-w-0"><p>{{.Name}}</p>{{if .Error}}<p class="mt-1 text-[12.5px] break-words text-neg">{{.Error}}</p>{{end}}</div>
</li>{{end}}
</ol>
{{template "room_foot" .}}{{end}}

{{define "close_bank"}}{{template "room_head" .}}
<div class="{{.Bank.ID}} mb-7 flex items-center gap-4">
<span class="grid size-11 place-items-center rounded-xl border border-line-strong bg-raised text-[17px] text-bank">{{initial .Bank.Name}}</span>
<div><h1 class="text-[24px]">Close {{.Bank.Name}}</h1><p class="mt-1 text-[13.5px] text-muted">Closing is permanent. The bank's accounts freeze, its online banking goes off and every Connect consent at the bank is revoked.</p></div>
</div>
{{range .Impact.Blockers}}<p class="mb-3 rounded-[10px] border border-warn/30 bg-warn/10 px-4 py-3 text-[13px] text-warn" role="alert">This bank cannot be closed yet: {{.}}.</p>{{end}}
{{if .Error}}<p class="mb-3 rounded-[10px] border border-neg/30 bg-neg/10 px-4 py-3 text-[13px] text-neg" role="alert">{{.Error}}</p>{{end}}
<div class="grid grid-cols-2 gap-3 lg:grid-cols-4">
{{template "stat" (list "Customers" (printf "%d" (len .Impact.Customers)) (count (len .Impact.Accounts) "account" "accounts"))}}
{{template "stat" (list "Deposits" (naira .Impact.Deposits) "")}}
{{template "stat" (list "Insured by NDIC" (naira .Impact.Insured) (printf "Up to %s per depositor" (naira .Impact.InsuredLimit)))}}
{{template "stat" (list "Connect consents" (printf "%d" (len .Impact.Connections)) "Revoked when the bank closes")}}
</div>
<form method="post" action="/ui/banks/{{.Bank.ID}}/close" class="mt-8">
<section class="rounded-2xl border border-line bg-panel p-6">
<h2 class="text-[15px]">Customers' money</h2>
<div class="mt-4 grid gap-4 sm:grid-cols-2">
<label class="block"><span class="mb-1.5 block text-[12.5px] text-muted">For every customer</span><select name="money" class="field">
<option value="transfer" {{if eq "transfer" (.Form.Get "money")}}selected{{end}}>Move the whole balance to another bank</option>
<option value="payout" {{if eq "payout" (.Form.Get "money")}}selected{{end}}>NDIC pays the insured part; the rest is a claim</option>
<option value="leave" {{if eq "leave" (.Form.Get "money")}}selected{{end}}>Leave it in the closed bank, unclaimed</option>
</select></label>
<label class="block"><span class="mb-1.5 block text-[12.5px] text-muted">Receiving bank</span><select name="to" class="field">{{range .Targets}}<option value="{{.ID}}" {{if eq .ID ($.Form.Get "to")}}selected{{end}}>{{.Name}}</option>{{else}}<option value="">No other bank can receive money</option>{{end}}</select></label>
</div>
<p class="mt-3 text-[12.5px] text-faint">Customers without an account at the receiving bank get a new current account there.</p>
</section>
{{if .Impact.Customers}}<section class="mt-6 rounded-2xl border border-line bg-panel">
<div class="border-b border-line px-5 py-3.5"><h2 class="text-[15px]">Each customer</h2></div>
<div class="overflow-x-auto"><table class="w-full min-w-[640px] border-collapse text-[13.5px]">
<thead><tr class="text-left text-[12px] text-faint"><th class="border-b border-line px-5 py-2.5 font-normal">Customer</th><th class="border-b border-line px-5 py-2.5 text-right font-normal">Deposits</th><th class="border-b border-line px-5 py-2.5 text-right font-normal">Insured</th><th class="border-b border-line px-5 py-2.5 text-right font-normal">Uninsured</th><th class="border-b border-line px-5 py-2.5 font-normal">Money</th></tr></thead>
<tbody>{{range .Impact.Customers}}<tr class="group">
<td class="border-b border-line px-5 py-3 group-last:border-b-0"><span class="block">{{.Name}}</span><span class="block text-[12.5px] text-muted">{{.Accounts}} account{{if ne .Accounts 1}}s{{end}}</span></td>
<td class="border-b border-line px-5 py-3 text-right group-last:border-b-0"><span class="num">{{naira .Deposits}}</span></td>
<td class="border-b border-line px-5 py-3 text-right group-last:border-b-0"><span class="num text-muted">{{naira .Insured}}</span></td>
<td class="border-b border-line px-5 py-3 text-right group-last:border-b-0"><span class="num text-muted">{{naira .Uninsured}}</span></td>
<td class="border-b border-line px-5 py-3 group-last:border-b-0">{{$c := $.Form.Get (printf "c_%s" .ID)}}<select name="c_{{.ID}}" class="h-8 rounded-lg border border-line bg-raised px-2.5 text-[13px] text-ink" aria-label="Money for {{.Name}}"><option value="">Same as above</option><option value="transfer" {{if eq $c "transfer"}}selected{{end}}>Move</option><option value="payout" {{if eq $c "payout"}}selected{{end}}>NDIC payout</option><option value="leave" {{if eq $c "leave"}}selected{{end}}>Leave</option></select></td>
</tr>{{end}}</tbody></table></div>
</section>{{end}}
<section class="mt-6 rounded-2xl border border-line bg-panel p-6">
<label class="block max-w-[360px]"><span class="mb-1.5 block text-[12.5px] text-muted">Type <span class="num text-ink">{{.Bank.ID}}</span> to confirm</span><input class="field font-mono" type="text" name="confirm" autocomplete="off" required></label>
<div class="mt-6 flex items-center justify-end gap-2"><a href="/ui/banks/{{.Bank.ID}}" class="btn text-muted hover:text-ink">Cancel</a><button type="submit" class="btn btn-danger" {{if .Impact.Blockers}}disabled{{end}}>Close {{.Bank.Name}}</button></div>
</section>
</form>
{{template "room_foot" .}}{{end}}

{{define "purge_bank"}}{{template "room_head" .}}
<div class="{{.Bank.ID}} mb-7 flex items-center gap-4">
<span class="grid size-11 place-items-center rounded-xl border border-line-strong bg-raised text-[17px] text-bank">{{initial .Bank.Name}}</span>
<div><h1 class="text-[24px]">Purge {{.Bank.Name}}</h1><p class="mt-1 text-[13.5px] text-muted">Purging deletes the bank from this setup for good. It cannot be undone.</p></div>
</div>
{{range .Impact.Blockers}}<p class="mb-3 rounded-[10px] border border-warn/30 bg-warn/10 px-4 py-3 text-[13px] text-warn" role="alert">This bank cannot be purged yet: {{.}}.</p>{{end}}
{{if .Error}}<p class="mb-3 rounded-[10px] border border-neg/30 bg-neg/10 px-4 py-3 text-[13px] text-neg" role="alert">{{.Error}}</p>{{end}}
<section class="rounded-2xl border border-line bg-panel p-6 text-[13.5px]">
<h2 class="text-[15px]">What gets deleted</h2>
<ul class="mt-3 list-disc space-y-1.5 pl-5 text-muted">
<li>The ledger database <span class="num text-ink">{{.Impact.Database}}</span> and its snapshot copies</li>
<li>{{with len .Impact.Snapshots}}{{.}} snapshot{{if ne . 1}}s{{end}} that include{{if eq . 1}}s{{end}} this bank, since they could no longer be restored{{else}}No snapshots include this bank{{end}}</li>
<li>Its entry in control.json and its online banking port</li>
</ul>
<h2 class="mt-6 text-[15px]">What stays</h2>
<ul class="mt-3 list-disc space-y-1.5 pl-5 text-muted">
<li>Its id, name, NIP code and NUBAN code stay reserved, because the other banks' history still refers to it</li>
<li>{{.Impact.Connections}} revoked Connect consent{{if ne .Impact.Connections 1}}s{{end}}, kept as history for partners</li>
</ul>
</section>
<form method="post" action="/ui/banks/{{.Bank.ID}}/purge" class="mt-6">
<section class="rounded-2xl border border-line bg-panel p-6">
<label class="block max-w-[360px]"><span class="mb-1.5 block text-[12.5px] text-muted">Type <span class="num text-ink">{{.Bank.ID}}</span> to confirm</span><input class="field font-mono" type="text" name="confirm" autocomplete="off" required></label>
<div class="mt-6 flex items-center justify-end gap-2"><a href="/ui/banks/{{.Bank.ID}}" class="btn text-muted hover:text-ink">Cancel</a><button type="submit" class="btn btn-danger" {{if .Impact.Blockers}}disabled{{end}}>Purge {{.Bank.Name}}</button></div>
</section>
</form>
{{template "room_foot" .}}{{end}}

{{define "new_bank"}}{{template "room_head" .}}
<div class="mb-7"><h1 class="text-[24px]">New bank</h1><p class="mt-1.5 text-[13.5px] text-muted">The bank gets its own ledger database, online banking site and listing in Beerus Connect. Codes and the port are assigned for you.</p></div>
<form method="post" action="/ui/banks" class="max-w-[640px] rounded-2xl border border-line bg-panel p-6">
{{if .Error}}<p class="mb-5 rounded-[10px] border border-neg/30 bg-neg/10 px-3 py-2.5 text-[13px] text-neg" role="alert">{{.Error}}</p>{{end}}
<div class="grid gap-4 sm:grid-cols-2">
<label class="block sm:col-span-2"><span class="mb-1.5 block text-[12.5px] text-muted">Name</span><input class="field" type="text" name="name" maxlength="40" value="{{.Form.Get "name"}}" required autofocus></label>
<label class="block"><span class="mb-1.5 block text-[12.5px] text-muted">Type</span><select name="type" class="field"><option value="commercial" {{if eq "commercial" (.Form.Get "type")}}selected{{end}}>Commercial</option><option value="digital" {{if eq "digital" (.Form.Get "type")}}selected{{end}}>Digital</option><option value="microfinance" {{if eq "microfinance" (.Form.Get "type")}}selected{{end}}>Microfinance</option></select></label>

<label class="block sm:col-span-2"><span class="mb-1.5 block text-[12.5px] text-muted">Settles through</span><select name="settles_through" class="field"><option value="">Not needed</option>{{range .Banks}}{{if ne .Type "microfinance"}}<option value="{{.ID}}" {{if eq .ID ($.Form.Get "settles_through")}}selected{{end}}>{{.Name}}</option>{{end}}{{end}}</select></label>
</div>
<div class="mt-6 flex items-center justify-end gap-2"><a href="/ui" class="btn text-muted hover:text-ink">Cancel</a><button type="submit" class="btn btn-primary">Create bank</button></div>
</form>
{{template "room_foot" .}}{{end}}

{{define "transfer"}}{{template "room_head" .}}
<div class="mb-7"><h1 class="text-[24px]">New transfer</h1><p class="mt-1.5 text-[13.5px] text-muted">Within a bank, transfers are internal. Between banks they go over NIP with the transfer fee, VAT and stamp duty.</p></div>
<form method="post" action="/ui/transfer" class="max-w-[640px] rounded-2xl border border-line bg-panel p-6">
{{if .Error}}<p class="mb-5 rounded-[10px] border border-neg/30 bg-neg/10 px-3 py-2.5 text-[13px] text-neg" role="alert">{{.Error}}</p>{{end}}
<div class="grid gap-4 sm:grid-cols-2">
<label class="block"><span class="mb-1.5 block text-[12.5px] text-muted">From bank</span><select name="from_bank" class="field" required>{{range .Banks}}<option value="{{.ID}}" {{if eq .ID ($.Form.Get "from_bank")}}selected{{end}}>{{.Name}}</option>{{end}}</select></label>
<label class="block"><span class="mb-1.5 block text-[12.5px] text-muted">From account number</span><input class="field font-mono" type="text" name="from" inputmode="numeric" maxlength="10" value="{{.Form.Get "from"}}" placeholder="10 digits" required></label>
<label class="block"><span class="mb-1.5 block text-[12.5px] text-muted">To bank</span><select name="to_bank" class="field" required>{{range .Banks}}<option value="{{.ID}}" {{if eq .ID ($.Form.Get "to_bank")}}selected{{end}}>{{.Name}}</option>{{end}}</select></label>
<label class="block"><span class="mb-1.5 block text-[12.5px] text-muted">To account number</span><input class="field font-mono" type="text" name="to" inputmode="numeric" maxlength="10" value="{{.Form.Get "to"}}" placeholder="10 digits" required></label>
<label class="block"><span class="mb-1.5 block text-[12.5px] text-muted">Amount (₦)</span><input class="field font-mono" type="text" name="amount" inputmode="decimal" value="{{.Form.Get "amount"}}" placeholder="25,000.00" required></label>
<label class="block"><span class="mb-1.5 block text-[12.5px] text-muted">Narration</span><input class="field" type="text" name="narration" value="{{.Form.Get "narration"}}" placeholder="Optional"></label>
</div>
<div class="mt-6 flex items-center justify-end gap-2"><a href="/ui" class="btn text-muted hover:text-ink">Cancel</a><button type="submit" class="btn btn-primary">Send transfer</button></div>
</form>
{{template "room_foot" .}}{{end}}

{{define "connections"}}{{template "room_head" .}}
<div class="mb-7"><h1 class="text-[24px]">Connections</h1><p class="mt-1.5 text-[13.5px] text-muted">Every consent an app holds through Beerus Connect.</p></div>
<section class="rounded-2xl border border-line bg-panel">
{{if .Rows}}<div class="overflow-x-auto"><table class="w-full min-w-[640px] border-collapse text-[13.5px]">
<thead><tr class="text-left text-[12px] text-faint"><th class="border-b border-line px-5 py-2.5 font-normal">App</th><th class="border-b border-line px-5 py-2.5 font-normal">Customer</th><th class="border-b border-line px-5 py-2.5 font-normal">Bank</th><th class="border-b border-line px-5 py-2.5 font-normal">Status</th><th class="border-b border-line px-5 py-2.5 font-normal">Consent until</th><th class="border-b border-line px-5 py-2.5 font-normal"></th></tr></thead>
<tbody>{{range .Rows}}<tr class="group">
<td class="border-b border-line px-5 py-3 group-last:border-b-0">{{.Partner}}<span class="num block text-[12px] text-faint">{{.ID}}</span></td>
<td class="border-b border-line px-5 py-3 group-last:border-b-0">{{.Customer}}<span class="block text-[12.5px] text-muted">{{.Accounts}} account{{if ne .Accounts 1}}s{{end}}</span></td>
<td class="border-b border-line px-5 py-3 text-muted group-last:border-b-0">{{.Bank}}</td>
<td class="border-b border-line px-5 py-3 group-last:border-b-0"><span class="badge badge-{{statusClass .Status}}">{{human .Status}}</span></td>
<td class="border-b border-line px-5 py-3 text-muted group-last:border-b-0">{{day .ExpiresAt}}</td>
<td class="border-b border-line px-5 py-3 text-right group-last:border-b-0">{{if ne .Status "revoked"}}<form method="post" action="/ui/connections/{{.ID}}/revoke"><button type="submit" class="btn btn-secondary btn-sm">Revoke at bank</button></form>{{end}}</td>
</tr>{{end}}</tbody></table></div>{{else}}<p class="px-5 py-10 text-center text-muted">No connections yet. Apps connect through the Connect widget.</p>{{end}}
</section>
{{template "room_foot" .}}{{end}}

{{define "webhooks"}}{{template "room_head" .}}
<div class="mb-7"><h1 class="text-[24px]">Webhooks</h1><p class="mt-1.5 text-[13.5px] text-muted">Deliveries, newest first, signed with Ed25519.</p></div>
<section class="rounded-2xl border border-line bg-panel">
{{if .Rows}}<div class="overflow-x-auto"><table class="w-full min-w-[640px] border-collapse text-[13.5px]">
<thead><tr class="text-left text-[12px] text-faint"><th class="border-b border-line px-5 py-2.5 font-normal">Event</th><th class="border-b border-line px-5 py-2.5 font-normal">Status</th><th class="border-b border-line px-5 py-2.5 font-normal">Attempts</th><th class="border-b border-line px-5 py-2.5 font-normal">Sent</th><th class="border-b border-line px-5 py-2.5 font-normal"></th></tr></thead>
<tbody>{{range .Rows}}<tr class="group">
<td class="border-b border-line px-5 py-3 group-last:border-b-0"><span class="num">{{.Type}}</span><span class="num block text-[12px] text-faint">{{.ID}}</span></td>
<td class="border-b border-line px-5 py-3 group-last:border-b-0"><span class="badge badge-{{statusClass .Status}}">{{human .Status}}</span>{{if .LastError}}<span class="mt-1 block text-[12px] text-faint">{{short .LastError 60}}</span>{{end}}</td>
<td class="border-b border-line px-5 py-3 text-muted group-last:border-b-0">{{.Attempts}}</td>
<td class="border-b border-line px-5 py-3 whitespace-nowrap text-muted group-last:border-b-0">{{when .CreatedReal}}</td>
<td class="border-b border-line px-5 py-3 text-right group-last:border-b-0"><form method="post" action="/ui/webhooks/{{.ID}}/replay"><button type="submit" class="btn btn-secondary btn-sm">Replay</button></form></td>
</tr>{{end}}</tbody></table></div>{{else}}<p class="px-5 py-10 text-center text-muted">No webhooks sent yet.</p>{{end}}
{{if or .Older .Newer}}<div class="flex items-center justify-between border-t border-line px-5 py-3 text-[13px]">
{{if .Newer}}<a href="?page={{add .Page -1}}" class="btn btn-secondary btn-sm">Newer</a>{{else}}<span></span>{{end}}<span class="text-muted">Page {{.Page}}</span>{{if .Older}}<a href="?page={{add .Page 1}}" class="btn btn-secondary btn-sm">Older</a>{{else}}<span></span>{{end}}
</div>{{end}}
</section>
{{template "room_foot" .}}{{end}}

{{define "notfound"}}{{template "room_head" .}}
<h1 class="text-[24px]">Not found</h1><p class="mt-1.5 text-[13.5px] text-muted">That page does not exist. <a href="/ui" class="text-ink underline underline-offset-4">Go to the overview</a>.</p>
{{template "room_foot" .}}{{end}}

{{define "room_login"}}<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>Sign in · Beerus</title><link rel="stylesheet" href="{{css}}"><script src="/assets/theme.js"></script><link rel="icon" href="/assets/icon.svg" type="image/svg+xml"></head>
<body class="min-h-screen"><div class="fixed top-3 right-4">{{themeToggle}}</div><main class="flex min-h-screen items-center justify-center px-4">
<form method="post" action="/ui/login" class="w-full max-w-[380px] rounded-2xl border border-line bg-panel p-7">
<div class="mb-6 flex items-center gap-2.5 text-[14px]">{{logo}}<span>Beerus</span></div>
<h1 class="text-[22px]">Control room</h1>
<p class="mt-2 text-[13.5px] text-muted">Paste the token from <span class="num">.beerus/control-token</span>, or open the link printed by <span class="num">beerus up</span>.</p>
{{if .Error}}<p class="mt-4 rounded-[10px] border border-neg/30 bg-neg/10 px-3 py-2.5 text-[13px] text-neg">{{.Error}}</p>{{end}}
<input class="field mt-5 font-mono" type="password" name="token" placeholder="ctl_…" autocomplete="off" required autofocus>
<button type="submit" class="btn btn-primary mt-4 w-full">Open control room</button>
</form></main></body></html>{{end}}
`
