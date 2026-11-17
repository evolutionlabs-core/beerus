package banktest

import "fmt"

func Session(code string, n int) string {
	return fmt.Sprintf("%s261114103000%012d", code, n)
}

func CreditBody(sid, to string, amount int64, narration string) string {
	return fmt.Sprintf(`{"session_id":%q,"from_code":"000014","from_bank_name":"Bank B","from_nuban":"9010000014","from_name":"CHIDI OKAFOR","to_nuban":%q,"amount_minor":%d,"narration":%q,"created_at":"2026-11-14T10:30:00+01:00"}`, sid, to, amount, narration)
}
