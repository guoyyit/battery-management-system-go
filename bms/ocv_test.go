package bms

import (
	"math"
	"testing"
)

func TestOCVTableRoundTrip(t *testing.T) {
	tbl := DefaultOCVTable()
	for _, soc := range []float64{0.01, 0.25, 0.5, 0.75, 0.99} {
		v := tbl.VocAtSOC(soc)
		got := tbl.SOCAtVoc(v)
		if math.Abs(got-soc) > 1e-9 {
			t.Fatalf("round trip at soc %.2f: got %.6f", soc, got)
		}
	}
}

func TestOCVTableMonotonicAndClamp(t *testing.T) {
	tbl := DefaultOCVTable()
	if tbl.VocAtSOC(-1) != tbl.Voltage[0] {
		t.Fatal("VocAtSOC must clamp below range")
	}
	if tbl.VocAtSOC(2) != tbl.Voltage[len(tbl.Voltage)-1] {
		t.Fatal("VocAtSOC must clamp above range")
	}
	if s := tbl.SOCAtVoc(0); s != 0 {
		t.Fatalf("SOCAtVoc below range = %v, want 0", s)
	}
	if s := tbl.SOCAtVoc(5); s != 1 {
		t.Fatalf("SOCAtVoc above range = %v, want 1", s)
	}
	prev := tbl.VocAtSOC(0)
	for i := 1; i <= 100; i++ {
		v := tbl.VocAtSOC(float64(i) / 100)
		if v < prev {
			t.Fatalf("OCV curve not monotone at %d", i)
		}
		prev = v
	}
}
