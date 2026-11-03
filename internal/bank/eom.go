package bank

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/bank/ledger"
	"github.com/evolutionlabs-core/beerus/internal/charges"
	"github.com/evolutionlabs-core/beerus/internal/clock"
)

type MonthEndResult struct {
	Accounts int
	Charged  int64
}

func (b *Bank) MonthEnd(ctx context.Context, month time.Month, year int, at time.Time) (MonthEndResult, error) {
	var res MonthEndResult
	from := clock.Date{Y: year, M: month, D: 1}
	to := from.AddDays(32)
	to = clock.Date{Y: to.Y, M: to.M, D: 1}
	label := strings.ToUpper(from.Start().Format("Jan 2006"))
	rows, err := b.L.Pool().Query(ctx, `SELECT a.code, a.product,
		(SELECT count(DISTINCT e.entry_id) FROM account_events e JOIN journal_entries je ON je.id = e.entry_id WHERE e.account_id = a.id AND e.posted_at >= $1 AND e.posted_at < $2 AND je.kind <> 'charge'),
		(SELECT coalesce(sum(je.limited_amount), 0) FROM journal_entries je WHERE je.limited_account = a.id AND je.posting_date >= $1 AND je.posting_date < $2
			AND NOT EXISTS (SELECT 1 FROM journal_entries r WHERE r.reverses = je.id))
		FROM gl_accounts a WHERE a.customer AND a.status NOT IN ('closed', 'frozen') ORDER BY a.id`, from.Start(), to.Start())
	if err != nil {
		return res, err
	}
	type acct struct {
		code, product string
		events        int64
		turnover      int64
	}
	list := []acct{}
	for rows.Next() {
		var a acct
		var product *string
		if err := rows.Scan(&a.code, &product, &a.events, &a.turnover); err != nil {
			rows.Close()
			return res, err
		}
		if product != nil {
			a.product = *product
		}
		list = append(list, a)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return res, err
	}
	key := fmt.Sprintf("%04d-%02d", year, int(month))
	var failed []error
	for _, a := range list {
		lines := []ledger.Line{}
		if a.events > 0 {
			sms := a.events * charges.SMSAlertFee
			if sms > charges.SMSMonthlyCap {
				sms = charges.SMSMonthlyCap
			}
			vat := charges.VAT(sms)
			lines = append(lines,
				dr(a.code, sms, "SMS ALERT CHARGES "+label), cr(FeeIncome, sms, "SMS ALERT CHARGES "+label),
				dr(a.code, vat, "VAT ON SMS ALERT CHARGES "+label), cr(VATPayable, vat, "VAT ON SMS ALERT CHARGES "+label))
		}
		if a.product == "current" && a.turnover > 0 {
			fee := (a.turnover*charges.MaintenancePerMille + 500) / 1000
			if fee > 0 {
				vat := charges.VAT(fee)
				lines = append(lines,
					dr(a.code, fee, "ACCOUNT MAINT FEE "+label), cr(FeeIncome, fee, "ACCOUNT MAINT FEE "+label),
					dr(a.code, vat, "VAT ON ACCOUNT MAINT FEE "+label), cr(VATPayable, vat, "VAT ON ACCOUNT MAINT FEE "+label))
			}
		}
		if len(lines) == 0 {
			continue
		}
		e := entry("eom:"+key+":"+a.code, "charge", at, "Month-end charges "+label, lines...)
		e.AllowOverdraft = true
		p, err := b.L.Post(ctx, e)
		if err != nil {
			failed = append(failed, fmt.Errorf("month end for %s: %w", a.code, err))
			continue
		}
		if !p.Replayed {
			res.Accounts++
			for _, ln := range lines {
				if ln.Account == a.code {
					res.Charged += ln.Amount
				}
			}
		}
	}
	return res, errors.Join(failed...)
}
