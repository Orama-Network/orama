package android

import (
	"testing"
	"time"
)

// PatchAgeMonths counts calendar months and refuses what is not YYYYMM.
func TestPatchAgeMonths(t *testing.T) {
	now := time.Date(2026, time.October, 27, 0, 0, 0, 0, time.UTC)
	for level, want := range map[int64]int64{202610: 0, 202609: 1, 202512: 10, 202410: 24} {
		if got, ok := PatchAgeMonths(level, now); !ok || got != want {
			t.Errorf("PatchAgeMonths(%d) = %d, %v; want %d", level, got, ok, want)
		}
	}
	for _, bad := range []int64{0, -202601, 202600, 202613, 20261027} {
		if _, ok := PatchAgeMonths(bad, now); ok {
			t.Errorf("PatchAgeMonths(%d) read it as YYYYMM", bad)
		}
	}
}
