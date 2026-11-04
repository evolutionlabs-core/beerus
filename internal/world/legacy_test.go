package world

import (
	"testing"

	"github.com/evolutionlabs-core/beerus/internal/charges"
)

func TestLegacyChargeLinesGetCategories(t *testing.T) {
	lines := []Line{
		{Key: "scn:transfer-0:20261029T160756.409:1:bank-a:1", Account: "bank-a:1", Amount: -2500000, Channel: "nip"},
		{Key: "scn:transfer-0:20261029T160756.409:2:bank-a:1", Account: "bank-a:1", Amount: -2500, Channel: "charge"},
		{Key: "scn:transfer-0:20261029T160756.409:3:bank-a:1", Account: "bank-a:1", Amount: -188, Channel: "charge"},
		{Key: "scn:transfer-0:20261029T160756.409:4:bank-a:1", Account: "bank-a:1", Amount: -5000, Channel: "charge"},
		{Key: "scn:transfer-0:20261029T160756.409:5:bank-b:2", Account: "bank-b:2", Amount: 2500000, Channel: "nip"},
		{Key: "scn:transfer-1:20251029T160756.409:1:bank-a:1", Account: "bank-a:1", Amount: -1000, Channel: "nip"},
		{Key: "scn:transfer-1:20251029T160756.409:2:bank-a:1", Account: "bank-a:1", Amount: -1000, Channel: "charge"},
		{Key: "scn:transfer-1:20251029T160756.409:3:bank-a:1", Account: "bank-a:1", Amount: -75, Channel: "charge"},
		{Key: "scn:transfer-1:20251029T160756.409:4:bank-b:2", Account: "bank-b:2", Amount: 1000, Channel: "nip"},
		{Key: "scn:transfer-1:20251029T160756.409:5:bank-b:2", Account: "bank-b:2", Amount: -5000, Channel: "charge"},
		{Key: "scn:transfer-2:20261029T160756.409:1:bank-a:1", Account: "bank-a:1", Amount: -2000000, Channel: "internal"},
		{Key: "scn:transfer-2:20261029T160756.409:2:bank-a:1", Account: "bank-a:1", Amount: -5000, Channel: "charge"},
		{Key: "scn:transfer-3:20261029T160756.409:2:bank-a:1", Account: "bank-a:1", Amount: -2500, Channel: "charge", Category: charges.CategoryFee},
	}
	categoriseLegacy(lines)
	want := map[int]string{1: charges.CategoryFee, 2: charges.CategoryVAT, 3: charges.CategoryStamp, 6: charges.CategoryFee, 7: charges.CategoryVAT, 9: charges.CategoryLevy, 11: charges.CategoryStamp, 12: charges.CategoryFee}
	for i, l := range lines {
		if l.Category != want[i] {
			t.Errorf("line %d (%s): category %q, want %q", i, l.Key, l.Category, want[i])
		}
	}
}
