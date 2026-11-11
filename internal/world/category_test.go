package world

import "testing"

func TestChargesCarryACategory(t *testing.T) {
	w, now := testWorld(t)
	charges, reversed := 0, 0
	for _, a := range w.Accounts() {
		for _, l := range w.LinesFor(a.Key, now) {
			switch {
			case l.Channel == "charge":
				charges++
				if l.Category == "" {
					t.Fatalf("charge %q on %s has no category", l.Narration, a.Key)
				}
			case l.Channel == "reversal" && l.Category != "":
				reversed++
			case l.Channel != "charge" && l.Category != "":
				t.Fatalf("%s line %q on %s has category %s", l.Channel, l.Narration, a.Key, l.Category)
			}
		}
	}
	if charges == 0 || reversed == 0 {
		t.Fatalf("%d charges and %d reversed charges in the history", charges, reversed)
	}
}
