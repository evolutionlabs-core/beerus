package connect

const bankTemplates = `
{{define "bank_doc"}}<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="color-scheme" content="dark light">
<title>{{.Title}} · {{.Bank.Name}}</title>
<link rel="stylesheet" href="{{css}}">
</head>
<body class="min-h-screen {{.Bank.ID}}">{{end}}

{{define "bank_head"}}{{template "bank_doc" .}}
<header class="sticky top-0 z-10 border-b border-line bg-canvas/85 backdrop-blur-md">
<div class="mx-auto flex h-15 max-w-[1040px] items-center gap-6 px-5">
<a href="/accounts" class="flex shrink-0 items-center gap-2.5"><span class="grid size-8 place-items-center rounded-[9px] border border-line-strong bg-raised text-[14px] text-bank">{{initial .Bank.Name}}</span><span class="text-[14.5px] tracking-tight">{{.Bank.Name}}</span></a>
<nav class="flex flex-1 items-center gap-1 overflow-x-auto text-[13.5px]">
<a href="/accounts" class="flex h-8 shrink-0 items-center rounded-full px-3 {{if eq .Nav "accounts"}}bg-raised text-ink{{else}}text-muted hover:text-ink{{end}}">Accounts</a>
<a href="/transfer" class="flex h-8 shrink-0 items-center rounded-full px-3 {{if eq .Nav "transfer"}}bg-raised text-ink{{else}}text-muted hover:text-ink{{end}}">Send money</a>
<a href="/apps" class="flex h-8 shrink-0 items-center rounded-full px-3 {{if eq .Nav "apps"}}bg-raised text-ink{{else}}text-muted hover:text-ink{{end}}">Connected apps</a>
</nav>
<div class="hidden items-center gap-3 text-[13px] text-muted sm:flex"><span class="truncate">{{.User}}</span>
<form method="post" action="/logout"><input type="hidden" name="csrf" value="{{.CSRF}}"><button type="submit" class="btn btn-secondary btn-sm">Sign out</button></form></div>
</div>
</header>
<main class="mx-auto max-w-[1040px] px-5 py-8">
{{end}}

{{define "bank_foot"}}</main>
</body>
</html>{{end}}

{{define "bank_auth_head"}}{{template "bank_doc" .}}
<main class="flex min-h-screen flex-col items-center px-4 pt-16 pb-10 sm:pt-24">
<div class="w-full max-w-[400px]">
<div class="mb-6 flex items-center gap-3"><span class="grid size-9 place-items-center rounded-[10px] border border-line-strong bg-raised text-[15px] text-bank">{{initial .Bank.Name}}</span><div class="leading-tight"><div class="text-[14px]">{{.Bank.Name}}</div><div class="text-[12.5px] text-muted">Online banking</div></div></div>
<section class="rounded-2xl border border-line bg-panel p-7">
{{end}}

{{define "bank_auth_foot"}}</section></div></main></body></html>{{end}}

{{define "bank_error"}}{{if .Error}}<p class="mt-4 rounded-[10px] border border-neg/30 bg-neg/10 px-3 py-2.5 text-[13px] text-neg" role="alert">{{.Error}}</p>{{end}}{{end}}

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

{{define "bank_verify"}}{{template "bank_auth_head" .}}
<h1 class="text-[22px]">Verify it's you</h1>
<p class="mt-2 text-[13.5px] text-muted">We sent a 6-digit code to the phone number on your account.</p>
<div class="mt-5 rounded-xl border border-line bg-raised px-3.5 py-3">
<div class="flex items-center justify-between text-[12px] text-muted"><span>Messages · {{.Bank.Name}}</span><span>now</span></div>
<p class="mt-1.5 text-[13.5px] leading-relaxed">Your {{.Bank.Name}} verification code is <span class="num text-ink">{{.OTP}}</span>. It expires in 5 minutes. Never share this code with anyone.</p>
</div>
{{template "bank_error" .}}
<form method="post" action="/verify" class="mt-5 space-y-4">
<label class="block"><span class="mb-1.5 block text-[12.5px] text-muted">Verification code</span><input class="field h-13 text-center font-mono text-[20px] tracking-[0.4em]" type="text" name="otp" inputmode="numeric" autocomplete="one-time-code" maxlength="6" required autofocus></label>
<button type="submit" class="btn btn-primary w-full">Verify</button>
</form>
{{template "bank_auth_foot" .}}{{end}}

{{define "bank_accounts"}}{{template "bank_head" .}}
<div class="mb-7 flex flex-wrap items-end justify-between gap-4">
<div><p class="text-[13px] text-muted">Total available</p><p class="mt-1 text-[30px] tracking-tight tabular-nums">{{naira .Total}}</p></div>
<a href="/transfer" class="btn btn-primary">Send money</a>
</div>
<div class="grid gap-3 sm:grid-cols-2">
{{range .Accounts}}<a href="/accounts/{{.NUBAN}}" class="group rounded-2xl border border-line bg-panel p-5 transition-colors hover:border-line-strong hover:bg-raised">
<div class="flex items-start justify-between gap-3"><div><p class="text-[14px]">{{title .Type}} account</p><p class="num mt-0.5 text-[12.5px] text-muted">{{.NUBAN}}</p></div>{{if .Pending}}<span class="badge badge-warn">{{.Pending}} pending</span>{{end}}</div>
<p class="mt-6 text-[12.5px] text-muted">Available balance</p>
<p class="mt-1 text-[24px] tracking-tight tabular-nums">{{naira .Available}}</p>
<p class="mt-1 text-[12.5px] text-faint">Ledger balance {{naira .Ledger}}</p>
</a>{{end}}
</div>
{{template "bank_foot" .}}{{end}}

{{define "bank_statement"}}{{template "bank_head" .}}
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
<td class="border-b border-line px-5 py-2.5 whitespace-nowrap text-muted group-last:border-b-0"><a href="{{.Href}}" class="block">{{when .At}}</a></td>
<td class="border-b border-line px-5 py-2.5 group-last:border-b-0"><a href="{{.Href}}" class="flex items-center gap-2"><span class="max-w-[440px] truncate">{{.Narration}}</span>{{if .Pending}}<span class="badge badge-warn">Pending</span>{{end}}</a></td>
<td class="border-b border-line px-5 py-2.5 text-right whitespace-nowrap group-last:border-b-0"><span class="num {{amtClass .Amount}}">{{signed .Amount}}</span></td>
<td class="border-b border-line px-5 py-2.5 text-right whitespace-nowrap group-last:border-b-0">{{if .Pending}}<span class="text-faint">—</span>{{else}}<span class="num text-muted">{{naira .BalanceAfter}}</span>{{end}}</td>
</tr>{{else}}<tr><td colspan="4" class="px-5 py-10 text-center text-muted">No transactions yet.</td></tr>{{end}}</tbody></table></div>
{{if or .Older .Newer}}<div class="flex items-center justify-between border-t border-line px-5 py-3 text-[13px]">
{{if .Newer}}<a href="?page={{add .Page -1}}" class="btn btn-secondary btn-sm">Newer</a>{{else}}<span></span>{{end}}<span class="text-muted">Page {{.Page}}</span>{{if .Older}}<a href="?page={{add .Page 1}}" class="btn btn-secondary btn-sm">Older</a>{{else}}<span></span>{{end}}
</div>{{end}}
</section>
{{template "bank_foot" .}}{{end}}

{{define "bank_tx"}}{{template "bank_head" .}}
{{if .Done}}<p class="mb-6 rounded-xl border border-pos/30 bg-pos/10 px-4 py-3 text-[13.5px] text-pos">Transfer successful.</p>{{end}}
<a href="/accounts/{{.A.NUBAN}}" class="text-[13px] text-muted hover:text-ink">← {{title .A.Type}} account · {{.A.NUBAN}}</a>
<div class="mt-4 mb-7 flex flex-wrap items-end justify-between gap-4">
<div><h1 class="text-[24px]">{{.L.Description}}</h1><p class="mt-1.5 text-[13.5px] text-muted">{{when .L.At}}</p></div>
<div class="text-right"><p class="num text-[30px] tracking-tight {{amtClass .L.Amount}}">{{signed .L.Amount}}</p>{{if .L.Pending}}<span class="badge badge-warn">Pending</span>{{else}}<span class="badge badge-ok">Successful</span>{{end}}</div>
</div>
<section class="rounded-2xl border border-line bg-panel">
<dl class="grid grid-cols-[150px_1fr] gap-x-4 gap-y-3 px-5 py-5 text-[13.5px]">
<dt class="text-muted">Narration</dt><dd class="num break-all">{{.L.Narration}}</dd>
<dt class="text-muted">Value date</dt><dd>{{.L.ValueDate}}</dd>
<dt class="text-muted">Reference</dt><dd class="num">{{if .L.BankRef}}{{.L.BankRef}}{{else}}—{{end}}</dd>
<dt class="text-muted">Session ID</dt><dd class="num break-all">{{if .L.SessionID}}{{.L.SessionID}}{{else}}—{{end}}</dd>
{{with .L.Counterparty}}<dt class="text-muted">{{if gt $.L.Amount 0}}From{{else}}To{{end}}</dt><dd>{{.Name}}{{if .AccountNumber}}<span class="block text-[12.5px] text-muted">{{.BankName}} · <span class="num">{{.AccountNumber}}</span></span>{{end}}</dd>{{end}}
<dt class="text-muted">Balance after</dt><dd>{{if .L.Pending}}—{{else}}<span class="num">{{naira .L.BalanceAfter}}</span>{{end}}</dd>
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
<select name="from" class="field" required>{{range .Accounts}}<option value="{{.NUBAN}}" {{if eq .NUBAN $.From}}selected{{end}}>{{title .Type}} · {{.NUBAN}} · {{naira .Available}}</option>{{end}}</select></label>
<fieldset class="sm:col-span-2"><legend class="mb-1.5 text-[12.5px] text-muted">To</legend>
<div class="grid grid-cols-2 gap-2">
<label class="flex cursor-pointer items-center gap-2.5 rounded-xl border border-line px-3.5 py-2.5 has-[:checked]:border-line-strong has-[:checked]:bg-raised"><input type="radio" name="mode" value="other" class="accent-[var(--primary)]" {{if ne (val .Form "mode") "own"}}checked{{end}}><span class="text-[13.5px]">Someone else</span></label>
<label class="flex cursor-pointer items-center gap-2.5 rounded-xl border border-line px-3.5 py-2.5 has-[:checked]:border-line-strong has-[:checked]:bg-raised"><input type="radio" name="mode" value="own" class="accent-[var(--primary)]" {{if eq (val .Form "mode") "own"}}checked{{end}}><span class="text-[13.5px]">My accounts</span></label>
</div></fieldset>
<label class="block"><span class="mb-1.5 block text-[12.5px] text-muted">Account number</span><input class="field font-mono" type="text" name="number" inputmode="numeric" maxlength="10" value="{{val .Form "number"}}" placeholder="10 digits"></label>
<label class="block"><span class="mb-1.5 block text-[12.5px] text-muted">Bank</span><select name="bank" class="field">{{range .Banks}}<option {{if eq . (val $.Form "bank")}}selected{{end}}>{{.}}</option>{{end}}</select></label>
<label class="block sm:col-span-2"><span class="mb-1.5 block text-[12.5px] text-muted">Beneficiary name</span><input class="field" type="text" name="name" value="{{val .Form "name"}}" placeholder="Only for accounts outside Beerus"></label>
<label class="block sm:col-span-2"><span class="mb-1.5 block text-[12.5px] text-muted">Or one of my accounts</span><select name="own" class="field"><option value="">Choose an account</option>{{range .Own}}<option value="{{.Key}}" {{if eq .Key (val $.Form "own")}}selected{{end}}>{{.Label}}</option>{{end}}</select></label>
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
{{range $k, $v := .Form}}{{if ne $k "csrf"}}{{range $v}}<input type="hidden" name="{{$k}}" value="{{.}}">{{end}}{{end}}{{end}}
<p class="text-[12.5px] text-muted">To</p>
<p class="mt-1 text-[16px]">{{.Q.ToName}}</p>
<p class="text-[13px] text-muted">{{.Q.ToBank}} · <span class="num">{{.Q.To}}</span></p>
<dl class="mt-6 grid grid-cols-[1fr_auto] gap-y-2.5 text-[13.5px]">
<dt class="text-muted">Amount</dt><dd class="num text-right">{{naira .Q.Amount}}</dd>
<dt class="text-muted">Transfer fee</dt><dd class="num text-right">{{naira .Q.Fee}}</dd>
<dt class="text-muted">VAT on fee</dt><dd class="num text-right">{{naira .Q.VAT}}</dd>
<dt class="text-muted">Stamp duty</dt><dd class="num text-right">{{naira .Q.StampDuty}}</dd>
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
{{if eq .Status "revoked"}}<span class="badge badge-bad">Access withdrawn</span>{{else if eq .Status "expired"}}<span class="badge badge-bad">Expired</span>{{else}}<form method="post" action="/apps/{{.ID}}/revoke"><input type="hidden" name="csrf" value="{{$.CSRF}}"><button type="submit" class="btn btn-secondary btn-sm">Withdraw access</button></form>{{end}}
</li>{{end}}</ul>{{else}}<p class="px-5 py-10 text-center text-muted">No apps are connected.</p>{{end}}
</section>
<section class="mt-8 rounded-2xl border border-line bg-panel">
<div class="border-b border-line px-5 py-3.5"><h2 class="text-[15px]">Access history</h2></div>
{{if .History}}<ul>{{range .History}}<li class="flex items-center justify-between gap-4 border-b border-line px-5 py-3 text-[13.5px] last:border-b-0"><span>{{.Partner}} read {{.Account}}</span><span class="text-muted">{{when .At}}</span></li>{{end}}</ul>{{else}}<p class="px-5 py-8 text-center text-muted">No access yet.</p>{{end}}
</section>
{{template "bank_foot" .}}{{end}}
`
