package connect

const roomTemplates = `
{{define "room_head"}}<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="color-scheme" content="dark light">
<title>{{.Title}} · Beerus</title>
<link rel="stylesheet" href="{{css}}">
</head>
<body class="min-h-screen">
<div class="grid min-h-screen grid-cols-1 lg:grid-cols-[236px_1fr]">
<aside class="flex items-center gap-1 overflow-x-auto border-b border-line px-3 py-2.5 lg:sticky lg:top-0 lg:block lg:h-screen lg:overflow-y-auto lg:border-r lg:border-b-0 lg:px-3.5 lg:py-5">
<a href="/ui" class="mr-3 flex shrink-0 items-center gap-2.5 px-2 text-[14px] tracking-tight lg:mr-0 lg:pb-5">{{logo}}<span>Beerus</span></a>
<nav class="flex shrink-0 gap-0.5 text-[13.5px] lg:block lg:space-y-0.5">
<a href="/ui" class="flex h-8 shrink-0 items-center rounded-lg px-2 whitespace-nowrap {{if eq .Nav "overview"}}bg-raised text-ink{{else}}text-muted hover:bg-raised hover:text-ink{{end}}">Overview</a>
<a href="/ui/transfer" class="flex h-8 shrink-0 items-center rounded-lg px-2 whitespace-nowrap {{if eq .Nav "transfer"}}bg-raised text-ink{{else}}text-muted hover:bg-raised hover:text-ink{{end}}">New transfer</a>
</nav>
<p class="mt-6 hidden px-2 pb-1.5 text-[11.5px] text-faint lg:block">Banks</p>
<nav class="flex shrink-0 gap-0.5 text-[13.5px] lg:block lg:space-y-0.5">
{{range .Banks}}<a href="/ui/banks/{{.ID}}" class="{{.ID}} flex h-8 shrink-0 items-center gap-2.5 rounded-lg px-2 whitespace-nowrap {{if eq $.Nav .ID}}bg-raised text-ink{{else}}text-muted hover:bg-raised hover:text-ink{{end}}"><span class="grid size-5 place-items-center rounded-md border border-line-strong text-[10.5px] text-bank">{{initial .Name}}</span>{{.Name}}</a>{{end}}
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
<form method="post" action="/ui/clock" class="flex items-center gap-2.5 text-[12.5px]">
<input type="hidden" name="back" value="{{.Back}}">
<span class="hidden text-muted sm:inline">{{when .Now}} WAT</span>
{{if isFrozen .Pace}}<span class="badge badge-warn">Clock paused</span>{{end}}
<select name="by" class="h-7.5 rounded-full border border-line bg-raised px-2.5 text-[12.5px] text-ink" aria-label="Advance the clock by">
<option value="1h">+1 hour</option><option value="1d">+1 day</option><option value="7d">+7 days</option><option value="30d">+30 days</option><option value="365d">+1 year</option>
</select>
<button type="submit" class="btn btn-secondary btn-sm">Advance</button>
</form>
</header>
<main class="max-w-[1180px] px-5 py-8 lg:px-8">
{{end}}

{{define "room_foot"}}</main>
</div>
</div>
</body>
</html>{{end}}

{{define "stat"}}<div class="rounded-2xl border border-line bg-panel px-5 py-4"><p class="text-[12.5px] text-muted">{{index . 0}}</p><p class="mt-2 text-[22px] tracking-tight tabular-nums">{{index . 1}}</p>{{if index . 2}}<p class="mt-1 text-[12px] text-faint">{{index . 2}}</p>{{end}}</div>{{end}}

{{define "lines"}}<div class="overflow-x-auto"><table class="w-full min-w-[640px] border-collapse text-[13.5px]">
<thead><tr class="text-left text-[12px] text-faint"><th class="border-b border-line px-5 py-2.5 font-normal">Date</th><th class="border-b border-line px-5 py-2.5 font-normal">Description</th><th class="border-b border-line px-5 py-2.5 font-normal">Account</th><th class="border-b border-line px-5 py-2.5 text-right font-normal">Amount</th></tr></thead>
<tbody>{{range .}}<tr class="group">
<td class="border-b border-line px-5 py-2.5 whitespace-nowrap text-muted group-last:border-b-0"><a href="{{.Href}}" class="block">{{when .At}}</a></td>
<td class="border-b border-line px-5 py-2.5 group-last:border-b-0"><a href="{{.Href}}" class="flex items-center gap-2 hover:underline hover:decoration-line-strong hover:underline-offset-4"><span class="max-w-[380px] truncate">{{.Description}}</span>{{if .Pending}}<span class="badge badge-warn">Pending</span>{{end}}</a></td>
<td class="border-b border-line px-5 py-2.5 whitespace-nowrap group-last:border-b-0"><span class="block">{{.Holder}}</span><span class="block text-[12.5px] text-muted">{{.Bank.Name}} · <span class="num">{{.Account.NUBAN}}</span></span></td>
<td class="border-b border-line px-5 py-2.5 text-right whitespace-nowrap group-last:border-b-0"><span class="num {{amtClass .Amount}}">{{signed .Amount}}</span></td>
</tr>{{end}}</tbody></table></div>{{end}}

{{define "overview"}}{{template "room_head" .}}
<div class="mb-7"><h1 class="text-[24px]">Overview</h1><p class="mt-1.5 text-[13.5px] text-muted">Business time {{when .Now}} WAT · world <span class="num">{{.WorldID}}</span></p></div>
<div class="grid grid-cols-2 gap-3 lg:grid-cols-4">
{{template "stat" (list "Customer deposits" (naira .Total) "Across all banks")}}
{{range .BankSums}}{{template "stat" (list .Name (naira .Deposits) (printf "%d accounts" .Accounts))}}{{end}}
{{template "stat" (list "Connections" (printf "%d active" .Active) (printf "%d in total" .Connections))}}
</div>
<section class="mt-8 rounded-2xl border border-line bg-panel">
<div class="flex items-center justify-between border-b border-line px-5 py-3.5"><h2 class="text-[15px]">Recent activity</h2><a href="/ui/transfer" class="btn btn-primary btn-sm">New transfer</a></div>
{{if .Recent}}{{template "lines" .Recent}}{{else}}<p class="px-5 py-10 text-center text-muted">No activity yet.</p>{{end}}
</section>
{{template "room_foot" .}}{{end}}

{{define "bank"}}{{template "room_head" .}}
<div class="{{.Bank.ID}} mb-7 flex items-center gap-4">
<span class="grid size-11 place-items-center rounded-xl border border-line-strong bg-raised text-[17px] text-bank">{{initial .Bank.Name}}</span>
<div><h1 class="text-[24px]">{{.Bank.Name}}</h1><p class="mt-1 text-[13.5px] text-muted">{{if eq .Bank.Style "digital"}}Digital bank{{else}}Commercial bank{{end}} · NIP code <span class="num">{{.Bank.NIPCode}}</span> · history kept {{.Bank.HistoryDays}} days</p></div>
{{if .Down}}<span class="badge badge-bad ml-auto">Unavailable</span>{{else}}<span class="badge badge-ok ml-auto">Operational</span>{{end}}
</div>
<div class="grid grid-cols-2 gap-3 lg:grid-cols-3">
{{template "stat" (list "Customer deposits" (naira .Total) "")}}
{{template "stat" (list "Accounts" (printf "%d" (len .Accounts)) "")}}
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
</section>
{{template "room_foot" .}}{{end}}

{{define "account"}}{{template "room_head" .}}
<div class="{{.A.Bank.ID}} mb-7 flex flex-wrap items-center gap-4">
<span class="grid size-11 place-items-center rounded-xl border border-line-strong bg-raised text-[17px] text-bank">{{initial .A.Bank.Name}}</span>
<div class="min-w-0"><h1 class="text-[24px]">{{.A.Holder}}</h1><p class="mt-1 text-[13.5px] text-muted"><a href="/ui/banks/{{.A.Bank.ID}}" class="hover:text-ink">{{.A.Bank.Name}}</a> · <span class="capitalize">{{.A.Type}}</span> · <span class="num">{{.A.NUBAN}}</span> · {{.A.Name}}</p></div>
<a href="/ui/transfer?from={{.A.Key}}" class="btn btn-primary ml-auto">Send money</a>
</div>
<div class="grid grid-cols-2 gap-3 lg:grid-cols-4">
{{template "stat" (list "Available balance" (naira .A.Available) "")}}
{{template "stat" (list "Ledger balance" (naira .A.Ledger) "")}}
{{template "stat" (list "Pending" (printf "%d" .A.Pending) "Lines not yet posted")}}
{{template "stat" (list "Statement lines" (printf "%d" .Count) "Since the account opened")}}
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
<td class="border-b border-line px-5 py-2.5 whitespace-nowrap text-muted group-last:border-b-0"><a href="{{.Href}}" class="block">{{when .At}}</a></td>
<td class="border-b border-line px-5 py-2.5 group-last:border-b-0"><a href="{{.Href}}" class="flex items-center gap-2"><span class="max-w-[420px] truncate">{{.Description}}</span>{{if .Pending}}<span class="badge badge-warn">Pending</span>{{end}}</a></td>
<td class="border-b border-line px-5 py-2.5 whitespace-nowrap text-muted group-last:border-b-0">{{human .Channel}}</td>
<td class="border-b border-line px-5 py-2.5 text-right whitespace-nowrap group-last:border-b-0"><span class="num {{amtClass .Amount}}">{{signed .Amount}}</span></td>
<td class="border-b border-line px-5 py-2.5 text-right whitespace-nowrap group-last:border-b-0">{{if .Pending}}<span class="text-faint">—</span>{{else}}<span class="num text-muted">{{naira .BalanceAfter}}</span>{{end}}</td>
</tr>{{else}}<tr><td colspan="5" class="px-5 py-10 text-center text-muted">No transactions yet.</td></tr>{{end}}</tbody></table></div>
{{if or .Older .Newer}}<div class="flex items-center justify-between border-t border-line px-5 py-3 text-[13px]">
{{if .Newer}}<a href="?page={{add .Page -1}}" class="btn btn-secondary btn-sm">Newer</a>{{else}}<span></span>{{end}}
<span class="text-muted">Page {{.Page}}</span>
{{if .Older}}<a href="?page={{add .Page 1}}" class="btn btn-secondary btn-sm">Older</a>{{else}}<span></span>{{end}}
</div>{{end}}
</section>
{{template "room_foot" .}}{{end}}

{{define "line"}}{{template "room_head" .}}
{{if .Flash}}<p class="mb-6 rounded-xl border border-pos/30 bg-pos/10 px-4 py-3 text-[13.5px] text-pos">Transfer sent. Both banks have posted it, and Beerus Connect has synced it.</p>{{end}}
<a href="/ui/accounts/{{pathq .A.Key}}" class="text-[13px] text-muted hover:text-ink">← {{.A.Holder}} · {{.A.Bank.Name}} {{.A.NUBAN}}</a>
<div class="mt-4 mb-7 flex flex-wrap items-end justify-between gap-4">
<div><h1 class="text-[24px]">{{.L.Description}}</h1><p class="mt-1.5 text-[13.5px] text-muted">{{when .L.At}} WAT</p></div>
<div class="text-right"><p class="num text-[30px] tracking-tight {{amtClass .L.Amount}}">{{signed .L.Amount}}</p>{{if .L.Pending}}<span class="badge badge-warn">Pending</span>{{else}}<span class="badge badge-ok">Posted</span>{{end}}</div>
</div>
<div class="grid gap-3 lg:grid-cols-[1.2fr_1fr]">
<section class="rounded-2xl border border-line bg-panel">
<div class="border-b border-line px-5 py-3.5"><h2 class="text-[15px]">Details</h2></div>
<dl class="grid grid-cols-[150px_1fr] gap-x-4 gap-y-3 px-5 py-4 text-[13.5px]">
<dt class="text-muted">Narration</dt><dd class="num break-all">{{.L.Narration}}</dd>
<dt class="text-muted">Booked</dt><dd>{{if .L.Pending}}Not yet{{else}}{{when .L.BookedAt}}{{end}}</dd>
<dt class="text-muted">Value date</dt><dd>{{.L.ValueDate}}</dd>
<dt class="text-muted">Channel</dt><dd>{{human .L.Channel}}</dd>
<dt class="text-muted">Bank reference</dt><dd class="num">{{if .L.BankRef}}{{.L.BankRef}}{{else}}—{{end}}</dd>
<dt class="text-muted">NIP session ID</dt><dd class="num break-all">{{if .L.SessionID}}{{.L.SessionID}}{{else}}—{{end}}</dd>
{{with .L.Counterparty}}<dt class="text-muted">Counterparty</dt><dd>{{.Name}}{{if .AccountNumber}}<span class="block text-[12.5px] text-muted">{{.BankName}} · <span class="num">{{.AccountNumber}}</span></span>{{end}}</dd>{{end}}
<dt class="text-muted">Balance after</dt><dd>{{if .L.Pending}}—{{else}}<span class="num">{{naira .L.BalanceAfter}}</span>{{end}}</dd>
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
<span>{{.Description}}<span class="block text-[12.5px] text-muted">{{.Bank.Name}} · <span class="num">{{.Account.NUBAN}}</span> · {{human .Channel}}</span></span>
<span class="num {{amtClass .Amount}}">{{signed .Amount}}</span>
</a></li>{{end}}
</ol>
</section>{{end}}
{{template "room_foot" .}}{{end}}

{{define "transfer"}}{{template "room_head" .}}
<div class="mb-7"><h1 class="text-[24px]">New transfer</h1><p class="mt-1.5 text-[13.5px] text-muted">Within a bank, transfers are internal. Between banks they go over NIP with the transfer fee, VAT and stamp duty.</p></div>
<form method="post" action="/ui/transfer" class="max-w-[640px] rounded-2xl border border-line bg-panel p-6">
{{if .Error}}<p class="mb-5 rounded-[10px] border border-neg/30 bg-neg/10 px-3 py-2.5 text-[13px] text-neg" role="alert">{{.Error}}</p>{{end}}
<div class="grid gap-4 sm:grid-cols-2">
<label class="block sm:col-span-2"><span class="mb-1.5 block text-[12.5px] text-muted">From</span>
<select name="from" class="field" required>{{range .Accounts}}<option value="{{.Key}}" {{if eq .Key $.From}}selected{{end}}>{{.Holder}} · {{.Bank.Name}} {{.NUBAN}} · {{naira .Available}}</option>{{end}}</select></label>
<label class="block sm:col-span-2"><span class="mb-1.5 block text-[12.5px] text-muted">To</span>
<select name="to" class="field" required><option value="" disabled {{if not $.To}}selected{{end}}>Choose an account</option>{{range .Accounts}}<option value="{{.Key}}" {{if eq .Key $.To}}selected{{end}}>{{.Holder}} · {{.Bank.Name}} {{.NUBAN}}</option>{{end}}<option value="other" {{if eq "other" $.To}}selected{{end}}>Another account…</option></select></label>
<label class="block"><span class="mb-1.5 block text-[12.5px] text-muted">Other account number</span><input class="field font-mono" type="text" name="other_number" inputmode="numeric" maxlength="10" placeholder="Only for another account"></label>
<label class="block"><span class="mb-1.5 block text-[12.5px] text-muted">Other bank</span><select name="other_bank" class="field">{{range .Others}}<option>{{.}}</option>{{end}}</select></label>
<label class="block sm:col-span-2"><span class="mb-1.5 block text-[12.5px] text-muted">Beneficiary name</span><input class="field" type="text" name="other_name" placeholder="Only for another account"></label>
<label class="block"><span class="mb-1.5 block text-[12.5px] text-muted">Amount (₦)</span><input class="field font-mono" type="text" name="amount" inputmode="decimal" placeholder="25,000.00" required></label>
<label class="block"><span class="mb-1.5 block text-[12.5px] text-muted">Narration</span><input class="field" type="text" name="narration" placeholder="Optional"></label>
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
<div class="mb-7"><h1 class="text-[24px]">Webhooks</h1><p class="mt-1.5 text-[13.5px] text-muted">The latest 100 deliveries, signed with Ed25519.</p></div>
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
</section>
{{template "room_foot" .}}{{end}}

{{define "notfound"}}{{template "room_head" .}}
<h1 class="text-[24px]">Not found</h1><p class="mt-1.5 text-[13.5px] text-muted">That page does not exist. <a href="/ui" class="text-ink underline underline-offset-4">Go to the overview</a>.</p>
{{template "room_foot" .}}{{end}}

{{define "room_login"}}<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>Sign in · Beerus</title><link rel="stylesheet" href="{{css}}"></head>
<body class="min-h-screen"><main class="flex min-h-screen items-center justify-center px-4">
<form method="post" action="/ui/login" class="w-full max-w-[380px] rounded-2xl border border-line bg-panel p-7">
<div class="mb-6 flex items-center gap-2.5 text-[14px]">{{logo}}<span>Beerus</span></div>
<h1 class="text-[22px]">Control room</h1>
<p class="mt-2 text-[13.5px] text-muted">Paste the token from <span class="num">.beerus/control-token</span>, or open the link printed by <span class="num">beerus up</span>.</p>
{{if .Error}}<p class="mt-4 rounded-[10px] border border-neg/30 bg-neg/10 px-3 py-2.5 text-[13px] text-neg">{{.Error}}</p>{{end}}
<input class="field mt-5 font-mono" type="password" name="token" placeholder="ctl_…" autocomplete="off" required autofocus>
<button type="submit" class="btn btn-primary mt-4 w-full">Open control room</button>
</form></main></body></html>{{end}}
`
