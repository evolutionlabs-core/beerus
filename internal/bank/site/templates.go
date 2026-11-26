package site

const templates = `
{{define "bank_doc"}}<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="color-scheme" content="dark light">
<title>{{.Title}} · {{.Bank.Name}}</title>
<link rel="stylesheet" href="{{css}}">
<script src="/assets/theme.js"></script>
</head>
<body class="min-h-screen {{.Bank.ID}}">{{end}}

{{define "bank_head"}}{{template "bank_doc" .}}
<header class="sticky top-0 z-10 border-b border-line bg-canvas/85 backdrop-blur-md">
<div class="mx-auto flex h-15 max-w-[1040px] items-center gap-6 px-5">
<a href="/accounts" class="flex shrink-0 items-center gap-2.5"><span class="text-[14.5px] tracking-tight">{{.Bank.Name}}</span></a>
{{if .Nav}}<nav class="flex flex-1 items-center gap-1 overflow-x-auto text-[13.5px]">
<a href="/accounts" class="flex h-8 shrink-0 items-center rounded-full px-3 {{if eq .Nav "accounts"}}bg-raised text-ink{{else}}text-muted hover:text-ink{{end}}">Accounts</a>
<a href="/transfer" class="flex h-8 shrink-0 items-center rounded-full px-3 {{if eq .Nav "transfer"}}bg-raised text-ink{{else}}text-muted hover:text-ink{{end}}">Send money</a>
<a href="/apps" class="flex h-8 shrink-0 items-center rounded-full px-3 {{if eq .Nav "apps"}}bg-raised text-ink{{else}}text-muted hover:text-ink{{end}}">Connected apps</a>
</nav>{{end}}
<div class="ml-auto flex items-center gap-2">{{themeToggle}}<div class="hidden items-center gap-3 text-[13px] text-muted sm:flex"><span class="truncate">{{.User}}</span>
<form method="post" action="/logout"><input type="hidden" name="csrf" value="{{.CSRF}}"><button type="submit" class="btn btn-secondary btn-sm">Sign out</button></form></div></div>
</div>
</header>
<main class="mx-auto max-w-[1040px] px-5 py-8">
{{end}}

{{define "bank_foot"}}</main>
</body>
</html>{{end}}

{{define "bank_auth_head"}}{{template "bank_doc" .}}
<div class="fixed top-3 right-4">{{themeToggle}}</div>
<main class="flex min-h-screen flex-col items-center px-4 pt-16 pb-10 sm:pt-24">
<div class="w-full max-w-[400px]">
<div class="mb-6"><div class="leading-tight"><div class="text-[14px]">{{.Bank.Name}}</div><div class="text-[12.5px] text-muted">Online banking</div></div></div>
<section class="rounded-2xl border border-line bg-panel p-7">
{{end}}

{{define "bank_auth_foot"}}</section></div></main></body></html>{{end}}

{{define "bank_error"}}{{if .Error}}<p class="mt-4 rounded-[10px] border border-neg/30 bg-neg/10 px-3 py-2.5 text-[13px] text-neg" role="alert">{{.Error}}</p>{{end}}{{end}}

{{define "bank_unavailable"}}{{template "bank_auth_head" .}}
<h1 class="text-[22px]">Temporarily unavailable</h1>
<p class="mt-2 text-[13.5px] text-muted">{{.Bank.Name}} online banking is not available right now. Please try again later.</p>
{{template "bank_auth_foot" .}}{{end}}

{{define "bank_login"}}{{template "bank_auth_head" .}}
<h1 class="text-[22px]">Sign in</h1>
<p class="mt-2 text-[13.5px] text-muted">Welcome back to {{.Bank.Name}} online banking.</p>
{{template "bank_error" .}}
<form method="post" action="/login" class="mt-6 space-y-4">
<label class="block"><span class="mb-1.5 block text-[12.5px] text-muted">Username</span><input class="field" type="text" name="username" autocomplete="username" required autofocus></label>
<label class="block"><span class="mb-1.5 block text-[12.5px] text-muted">Password</span><input class="field" type="password" name="password" autocomplete="current-password" required></label>
<button type="submit" class="btn btn-primary mt-2 w-full">Continue</button>
</form>
{{template "bank_auth_foot" .}}{{end}}

{{define "bank_accounts"}}{{template "bank_head" .}}
<div class="mb-7 flex flex-wrap items-end justify-between gap-4">
<div><p class="text-[13px] text-muted">Total available</p><p class="mt-1 text-[30px] tracking-tight tabular-nums">{{naira .Total}}</p></div>
<a href="/transfer" class="btn btn-primary">Send money</a>
</div>
<div class="grid gap-3 sm:grid-cols-2">
{{range .Accounts}}<a href="/accounts/{{.NUBAN}}" class="group rounded-2xl border border-line bg-panel p-5 transition-colors hover:border-line-strong hover:bg-raised">
<div class="flex items-start justify-between gap-3"><div><p class="text-[14px]">{{title .Type}} account</p><p class="num mt-0.5 text-[12.5px] text-muted">{{.NUBAN}}</p></div></div>
<p class="mt-6 text-[12.5px] text-muted">Available balance</p>
<p class="mt-1 text-[24px] tracking-tight tabular-nums">{{naira .Available}}</p>
<p class="mt-1 text-[12.5px] text-faint">Ledger balance {{naira .Ledger}}</p>
</a>{{end}}
</div>
{{template "bank_foot" .}}{{end}}

{{define "bank_statement"}}{{template "bank_head" .}}
{{with .Transfer}}<p class="mb-6 rounded-xl border border-pos/30 bg-pos/10 px-4 py-3 text-[13.5px] text-pos">{{.}}</p>{{end}}
<a href="/accounts" class="text-[13px] text-muted hover:text-ink">← Accounts</a>
<div class="mt-4 mb-7 flex flex-wrap items-end justify-between gap-4">
<div><h1 class="text-[24px]">{{title .A.Type}} account</h1><p class="mt-1 text-[13.5px] text-muted"><span class="num">{{.A.NUBAN}}</span> · {{.A.Name}}</p></div>
<div class="text-right"><p class="text-[12.5px] text-muted">Available balance</p><p class="text-[26px] tracking-tight tabular-nums">{{naira .A.Available}}</p></div>
</div>
<div class="mb-4 flex gap-2"><a href="/transfer?from={{.A.NUBAN}}" class="btn btn-primary btn-sm">Send money</a></div>
<section class="rounded-2xl border border-line bg-panel">
<div class="overflow-x-auto"><table class="w-full min-w-[620px] border-collapse text-[13.5px]">
<thead><tr class="text-left text-[12px] text-faint"><th class="border-b border-line px-5 py-2.5 font-normal">Date</th><th class="border-b border-line px-5 py-2.5 font-normal">Description</th><th class="border-b border-line px-5 py-2.5 text-right font-normal">Amount</th><th class="border-b border-line px-5 py-2.5 text-right font-normal">Balance</th></tr></thead>
<tbody>{{range .Lines}}<tr class="group hover:bg-raised">
<td class="border-b border-line px-5 py-2.5 whitespace-nowrap text-muted group-last:border-b-0"><a href="{{.Href}}" class="block">{{when .BookedAt}}</a></td>
<td class="border-b border-line px-5 py-2.5 group-last:border-b-0"><a href="{{.Href}}" class="flex items-center gap-2"><span class="max-w-[440px] truncate">{{.Narration}}</span></a></td>
<td class="border-b border-line px-5 py-2.5 text-right whitespace-nowrap group-last:border-b-0"><span class="num {{amtClass .Amount}}">{{signed .Amount}}</span></td>
<td class="border-b border-line px-5 py-2.5 text-right whitespace-nowrap group-last:border-b-0"><span class="num text-muted">{{naira .BalanceAfter}}</span></td>
</tr>{{else}}<tr><td colspan="4" class="px-5 py-10 text-center text-muted">No transactions yet.</td></tr>{{end}}</tbody></table></div>
{{if or .Next .First}}<div class="flex items-center justify-between border-t border-line px-5 py-3 text-[13px]">
{{if .First}}<a href="?" class="btn btn-secondary btn-sm">Newest</a>{{else}}<span></span>{{end}}{{if .Next}}<a href="?cursor={{.Next}}" class="btn btn-secondary btn-sm">Older</a>{{else}}<span></span>{{end}}
</div>{{end}}
</section>
{{template "bank_foot" .}}{{end}}

{{define "bank_tx"}}{{template "bank_head" .}}
<a href="/accounts/{{.A.NUBAN}}" class="text-[13px] text-muted hover:text-ink">← {{title .A.Type}} account · {{.A.NUBAN}}</a>
<div class="mt-4 mb-7 flex flex-wrap items-end justify-between gap-4">
<div><h1 class="text-[24px]">{{.L.Description}}</h1><p class="mt-1.5 text-[13.5px] text-muted">{{when .L.BookedAt}}</p></div>
<div class="text-right"><p class="num text-[30px] tracking-tight {{amtClass .L.Amount}}">{{signed .L.Amount}}</p><span class="badge badge-ok">Posted</span></div>
</div>
<section class="rounded-2xl border border-line bg-panel">
<dl class="grid grid-cols-[150px_1fr] gap-x-4 gap-y-3 px-5 py-5 text-[13.5px]">
<dt class="text-muted">Narration</dt><dd class="num break-all">{{.L.Narration}}</dd>
<dt class="text-muted">Value date</dt><dd>{{.L.ValueDate}}</dd>
<dt class="text-muted">Reference</dt><dd class="num">{{if .L.BankRef}}{{.L.BankRef}}{{else}}—{{end}}</dd>
<dt class="text-muted">Session ID</dt><dd class="num break-all">{{if .L.SessionID}}{{.L.SessionID}}{{else}}—{{end}}</dd>
{{with .L.Counterparty}}<dt class="text-muted">{{if gt $.L.Amount 0}}From{{else}}To{{end}}</dt><dd>{{.Name}}{{if .AccountNumber}}<span class="block text-[12.5px] text-muted">{{.BankName}} · <span class="num">{{.AccountNumber}}</span></span>{{end}}</dd>{{end}}
<dt class="text-muted">Balance after</dt><dd><span class="num">{{naira .L.BalanceAfter}}</span></dd>
</dl>
</section>
{{if .Related}}<section class="mt-3 rounded-2xl border border-line bg-panel">
<div class="border-b border-line px-5 py-3.5"><h2 class="text-[15px]">Charges on this transfer</h2></div>
<ul>{{range .Related}}<li class="border-b border-line last:border-b-0"><a href="{{.Href}}" class="flex items-center justify-between px-5 py-3 text-[13.5px] hover:bg-raised"><span>{{.Description}}</span><span class="num {{amtClass .Amount}}">{{signed .Amount}}</span></a></li>{{end}}</ul>
</section>{{end}}
{{template "bank_foot" .}}{{end}}

{{define "bank_transfer"}}{{template "bank_head" .}}
<div class="mb-7"><h1 class="text-[24px]">Send money</h1><p class="mt-1.5 text-[13.5px] text-muted">Transfers to other banks use NIP and arrive instantly. You'll see the charges before you confirm.</p></div>
<form method="post" action="/transfer/review" class="max-w-[620px] rounded-2xl border border-line bg-panel p-6">
<input type="hidden" name="csrf" value="{{.CSRF}}">
{{template "bank_error" .}}
<div class="grid gap-4 sm:grid-cols-2">
<label class="block sm:col-span-2"><span class="mb-1.5 block text-[12.5px] text-muted">From</span>
<select name="from" class="field" required>{{range .Accounts}}<option value="{{.NUBAN}}" {{if eq .NUBAN (val $.Form "from")}}selected{{end}}>{{title .Type}} · {{.NUBAN}} · {{naira .Available}}</option>{{end}}</select></label>
<fieldset class="sm:col-span-2"><legend class="mb-1.5 text-[12.5px] text-muted">To</legend>
<div class="grid grid-cols-2 gap-2">
<label class="flex cursor-pointer items-center gap-2.5 rounded-xl border border-line px-3.5 py-2.5 has-[:checked]:border-line-strong has-[:checked]:bg-raised"><input type="radio" name="mode" value="other" class="accent-[var(--primary)]" {{if ne (val .Form "mode") "own"}}checked{{end}}><span class="text-[13.5px]">Someone else</span></label>
<label class="flex cursor-pointer items-center gap-2.5 rounded-xl border border-line px-3.5 py-2.5 has-[:checked]:border-line-strong has-[:checked]:bg-raised"><input type="radio" name="mode" value="own" class="accent-[var(--primary)]" {{if eq (val .Form "mode") "own"}}checked{{end}}><span class="text-[13.5px]">My accounts</span></label>
</div></fieldset>
<label class="block"><span class="mb-1.5 block text-[12.5px] text-muted">Account number</span><input class="field font-mono" type="text" name="number" inputmode="numeric" maxlength="10" value="{{val .Form "number"}}" placeholder="10 digits"></label>
<label class="block"><span class="mb-1.5 block text-[12.5px] text-muted">Bank</span><select name="bank" class="field">{{range .Banks}}<option value="{{.ID}}" {{if eq .ID (val $.Form "bank")}}selected{{end}}>{{.Name}}</option>{{end}}</select></label>
<label class="block sm:col-span-2"><span class="mb-1.5 block text-[12.5px] text-muted">Or one of my accounts</span><select name="own" class="field"><option value="">Choose an account</option>{{range .Accounts}}<option value="{{.NUBAN}}" {{if eq .NUBAN (val $.Form "own")}}selected{{end}}>{{title .Type}} · {{.NUBAN}}</option>{{end}}</select></label>
<label class="block"><span class="mb-1.5 block text-[12.5px] text-muted">Amount (₦)</span><input class="field font-mono" type="text" name="amount" inputmode="decimal" value="{{val .Form "amount"}}" placeholder="25,000.00" required></label>
<label class="block"><span class="mb-1.5 block text-[12.5px] text-muted">Narration</span><input class="field" type="text" name="narration" value="{{val .Form "narration"}}" placeholder="Optional"></label>
</div>
<div class="mt-6 flex justify-end"><button type="submit" class="btn btn-primary">Continue</button></div>
</form>
{{template "bank_foot" .}}{{end}}

{{define "bank_review"}}{{template "bank_head" .}}
<div class="mb-7"><h1 class="text-[24px]">Review transfer</h1><p class="mt-1.5 text-[13.5px] text-muted">Check the details. This can't be undone.</p></div>
<form method="post" action="/transfer/confirm" class="max-w-[520px] rounded-2xl border border-line bg-panel p-6">
<input type="hidden" name="csrf" value="{{.CSRF}}">
<input type="hidden" name="key" value="{{.Key}}">
{{range $k, $v := .Form}}{{if and (ne $k "csrf") (ne $k "key")}}{{range $v}}<input type="hidden" name="{{$k}}" value="{{.}}">{{end}}{{end}}{{end}}
<p class="text-[12.5px] text-muted">To</p>
<p class="mt-1 text-[16px]">{{.Q.ToName}}</p>
<p class="text-[13px] text-muted">{{.ToBank}} · <span class="num">{{.Q.To}}</span></p>
<dl class="mt-6 grid grid-cols-[1fr_auto] gap-y-2.5 text-[13.5px]">
<dt class="text-muted">Amount</dt><dd class="num text-right">{{naira .Q.Amount}}</dd>
{{if .Q.Fee}}<dt class="text-muted">Transfer fee</dt><dd class="num text-right">{{naira .Q.Fee}}</dd>{{end}}
{{if .Q.VAT}}<dt class="text-muted">VAT on fee</dt><dd class="num text-right">{{naira .Q.VAT}}</dd>{{end}}
{{if .Q.StampDuty}}<dt class="text-muted">Stamp duty</dt><dd class="num text-right">{{naira .Q.StampDuty}}</dd>{{end}}
<dt class="border-t border-line pt-2.5">Total debit</dt><dd class="num border-t border-line pt-2.5 text-right">{{naira .Q.TotalDebit}}</dd>
<dt class="text-muted">Balance after</dt><dd class="num text-right text-muted">{{naira .Q.BalanceAfter}}</dd>
</dl>
<div class="mt-6 grid grid-cols-2 gap-2"><a href="/transfer" class="btn btn-secondary w-full">Back</a><button type="submit" class="btn btn-primary w-full">Send {{naira .Q.Amount}}</button></div>
</form>
{{template "bank_foot" .}}{{end}}

{{define "bank_apps"}}{{template "bank_head" .}}
{{if .Revoked}}<p class="mb-6 rounded-xl border border-pos/30 bg-pos/10 px-4 py-3 text-[13.5px] text-pos">Access withdrawn. The app can no longer read your accounts.</p>{{end}}
<div class="mb-7"><h1 class="text-[24px]">Connected apps</h1><p class="mt-1.5 text-[13.5px] text-muted">Apps you've allowed to read your account details, balances and transactions. You can withdraw access at any time.</p></div>
<section class="rounded-2xl border border-line bg-panel">
{{if .Apps}}<ul>{{range .Apps}}<li class="flex flex-wrap items-center gap-4 border-b border-line px-5 py-4 last:border-b-0">
<div class="min-w-0 flex-1"><p class="text-[14px]">{{.Partner}} <span class="text-muted">via {{$.Via}}</span></p>
<p class="mt-0.5 text-[12.5px] text-muted">{{range $i, $a := .Accounts}}{{if $i}}, {{end}}{{$a}}{{end}}</p>
<p class="mt-0.5 text-[12.5px] text-faint">Allowed {{day .Since}}{{if not .Until.IsZero}} · until {{day .Until}}{{end}}{{if not .LastAccess.IsZero}} · last accessed {{when .LastAccess}}{{end}}</p></div>
{{if eq .Status "revoked"}}<span class="badge badge-bad">Access withdrawn</span>{{else if eq .Status "expired"}}<span class="badge badge-bad">Expired</span>{{else if eq .Status "dormant"}}<span class="badge badge-bad">Not used for a while</span>{{else if eq .Status "renew"}}<span class="badge badge-bad">Renewal needed</span>{{else}}<form method="post" action="/apps/{{.ID}}/revoke"><input type="hidden" name="csrf" value="{{$.CSRF}}"><button type="submit" class="btn btn-secondary btn-sm">Withdraw access</button></form>{{end}}
</li>{{end}}</ul>{{else}}<p class="px-5 py-10 text-center text-muted">No apps are connected.</p>{{end}}
</section>
<section class="mt-8 rounded-2xl border border-line bg-panel">
<div class="border-b border-line px-5 py-3.5"><h2 class="text-[15px]">Access history</h2></div>
{{if .History}}<ul>{{range .History}}<li class="flex items-center justify-between gap-4 border-b border-line px-5 py-3 text-[13.5px] last:border-b-0"><span>{{.Partner}} read {{.Account}}</span><span class="text-muted">{{when .At}}</span></li>{{end}}</ul>{{else}}<p class="px-5 py-8 text-center text-muted">No access yet.</p>{{end}}
{{if or .Older .Newer}}<div class="flex items-center justify-between border-t border-line px-5 py-3 text-[13px]">
{{if .Newer}}<a href="?page={{add .Page -1}}" class="btn btn-secondary btn-sm">Newer</a>{{else}}<span></span>{{end}}<span class="text-muted">Page {{.Page}}</span>{{if .Older}}<a href="?page={{add .Page 1}}" class="btn btn-secondary btn-sm">Older</a>{{else}}<span></span>{{end}}
</div>{{end}}
</section>
{{template "bank_foot" .}}{{end}}

{{define "bank_oauth_error"}}{{template "bank_auth_head" .}}
<h1 class="text-[22px]">Request not valid</h1>
<p class="mt-2 text-[13.5px] text-muted">{{.Error}}</p>
{{template "bank_auth_foot" .}}{{end}}

{{define "bank_consent"}}{{template "bank_head" .}}
<div class="mx-auto max-w-[420px] space-y-7 pt-6">
<div>
<h1 class="text-[24px] leading-snug font-medium tracking-tight">Allow {{.Partner}} to see your account?</h1>
<p class="mt-2 text-[14px] text-muted">{{.Partner}} asked through {{.Client.Name}}.</p>
</div>
<ul class="space-y-3.5 text-[14px] leading-normal">
<li class="flex gap-3"><svg class="mt-0.5 size-[18px] shrink-0" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M2 12s3.5-7 10-7 10 7 10 7-3.5 7-10 7S2 12 2 12z"/><circle cx="12" cy="12" r="3"/></svg><span>{{.Partner}} sees your {{.Scope}}</span></li>
<li class="flex gap-3"><svg class="mt-0.5 size-[18px] shrink-0" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><rect x="5" y="11" width="14" height="9" rx="2"/><path d="M8 11V8a4 4 0 0 1 8 0v3"/></svg><span>{{.Partner}} can't move your money</span></li>
<li class="flex gap-3"><svg class="mt-0.5 size-[18px] shrink-0" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><circle cx="12" cy="12" r="9"/><path d="M12 7v5l3 2"/></svg><span>Access ends {{.Until}}. Stop it any time in Connected apps.</span></li>
</ul>
{{template "bank_error" .}}
<form method="post" action="/authorize/consent" class="space-y-5">
<input type="hidden" name="csrf" value="{{.CSRF}}">
<fieldset class="space-y-2">
<legend class="mb-2 text-[12.5px] text-muted">{{if gt (len .Choices) 1}}Accounts{{else}}Account{{end}}</legend>
{{range .Choices}}<label class="flex cursor-pointer items-center gap-3 rounded-xl border border-line px-4 py-3.5 transition-colors hover:border-line-strong has-[:checked]:border-line-strong has-[:checked]:bg-panel">
<input type="checkbox" name="account" value="{{.ID}}" class="size-4 accent-[var(--primary)]" {{if .Checked}}checked{{end}}>
<span class="flex-1"><span class="block">{{.Name}}</span><span class="block text-[12.5px] text-muted capitalize">{{.Type}}</span></span>
<span class="num text-[13px] text-muted">•••• {{.Last4}}</span>
</label>{{end}}
</fieldset>
<div class="grid grid-cols-2 gap-2">
<button type="submit" name="action" value="decline" class="btn btn-secondary w-full">Decline</button>
<button type="submit" name="action" value="approve" class="btn btn-primary w-full">Approve</button>
</div>
<details class="group border-t border-line pt-4 text-[13px]">
<summary class="flex cursor-pointer list-none items-center justify-between text-muted [&::-webkit-details-marker]:hidden">More details<svg class="size-3.5 transition-transform group-open:rotate-180" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M6 9l6 6 6-6"/></svg></summary>
<dl class="mt-3.5 grid grid-cols-[112px_1fr] gap-x-4 gap-y-2 leading-normal">
<dt class="text-muted">Shared with</dt><dd>{{.Client.LegalName}}, for {{.PartnerLegal}}</dd>
<dt class="text-muted">Registry no.</dt><dd class="num">{{.Client.RegistryID}} · {{.Client.CAC}}</dd>
<dt class="text-muted">Purpose</dt><dd>{{.Purpose}}. Read only.</dd>
<dt class="text-muted">History from</dt><dd>{{.Start}}{{if .Capped}} <span class="text-muted">· {{$.Bank.Name}} shares from {{.Effective}}</span>{{end}}</dd>
<dt class="text-muted">Duration</dt><dd>{{count .Months "month" "months"}}</dd>
<dt class="text-muted">Updates</dt><dd>Several times a day, and when {{.Partner}} asks</dd>
<dt class="text-muted">If you stop it</dt><dd>{{.Client.Name}} deletes your data within 30 days. {{.Partner}} keeps records already in your books.</dd>
</dl>
<label class="mt-3.5 flex cursor-pointer items-start gap-2.5">
<input type="checkbox" name="analytics" value="yes" class="mt-0.5 accent-[var(--primary)]">
<span>Also share anonymised analytics <span class="text-muted">(optional)</span></span>
</label>
</details>
</form>
</div>
{{template "bank_foot" .}}{{end}}
`
