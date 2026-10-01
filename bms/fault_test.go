package bms

import (
	"testing"
	"time"
)

func TestFaultLogAppendPageSinceSeq(t *testing.T) {
	log := NewFaultLog(nil, 1)
	base := time.Unix(1000, 0)
	for i := 0; i < 5; i++ {
		rec := log.Append(FaultRecord{At: base.Add(time.Duration(i) * time.Second), Type: FaultOvertemp, Tripped: true})
		if rec.Seq != int64(i+1) {
			t.Fatalf("seq = %d, want %d", rec.Seq, i+1)
		}
	}
	if log.NextSeq() != 6 {
		t.Fatalf("next seq = %d", log.NextSeq())
	}

	page1 := log.Page(0, 2)
	if len(page1) != 2 || page1[0].Seq != 1 || page1[1].Seq != 2 {
		t.Fatalf("page1 wrong: %+v", page1)
	}
	page2 := log.Page(2, 2)
	if len(page2) != 2 || page2[0].Seq != 3 {
		t.Fatalf("page2 wrong: %+v", page2)
	}
	rest := log.Page(4, 0)
	if len(rest) != 1 || rest[0].Seq != 5 {
		t.Fatalf("tail page wrong: %+v", rest)
	}
	if got := log.SinceSeq(3); len(got) != 2 || got[0].Seq != 4 || got[1].Seq != 5 {
		t.Fatalf("SinceSeq wrong: %+v", got)
	}
}

func TestFaultLogRestoreContinuesSequences(t *testing.T) {
	existing := []FaultRecord{{Seq: 10, Type: FaultOvervoltage, Tripped: true}}
	log := NewFaultLog(existing, 0)
	if got := log.NextSeq(); got != 11 {
		t.Fatalf("restored next seq = %d, want 11", got)
	}
	rec := log.Append(FaultRecord{Type: FaultUndervoltage, Tripped: false})
	if rec.Seq != 11 {
		t.Fatalf("appended seq = %d, want 11", rec.Seq)
	}
}
