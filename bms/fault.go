package bms

import "time"

// FaultRecord is one immutable history entry (trip or clear).
type FaultRecord struct {
	Seq         int64
	At          time.Time
	Type        FaultType
	Cell        int
	Voltage     float64
	Temperature float64
	Current     float64
	Tripped     bool // false = clear event
}

// FaultLog is an append-only, time-ordered, pageable history.
type FaultLog struct {
	records []FaultRecord
	nextSeq int64
}

// NewFaultLog restores a log; nextSeq continues numbering after records.
func NewFaultLog(records []FaultRecord, nextSeq int64) *FaultLog {
	cp := append([]FaultRecord(nil), records...)
	if nextSeq <= 0 {
		nextSeq = 1
		for _, r := range cp {
			if r.Seq >= nextSeq {
				nextSeq = r.Seq + 1
			}
		}
	}
	return &FaultLog{records: cp, nextSeq: nextSeq}
}

// Append assigns a sequence number and stores the record.
func (l *FaultLog) Append(rec FaultRecord) FaultRecord {
	rec.Seq = l.nextSeq
	l.nextSeq++
	l.records = append(l.records, rec)
	return rec
}

// Page returns records ordered by time, newest-last, supporting offset/limit.
// Non-positive limit means "all remaining".
func (l *FaultLog) Page(offset, limit int) []FaultRecord {
	if offset < 0 {
		offset = 0
	}
	if offset >= len(l.records) {
		return nil
	}
	end := len(l.records)
	if limit > 0 && offset+limit < end {
		end = offset + limit
	}
	out := make([]FaultRecord, end-offset)
	copy(out, l.records[offset:end])
	return out
}

// SinceSeq returns records with sequence number strictly greater than seq.
func (l *FaultLog) SinceSeq(seq int64) []FaultRecord {
	var out []FaultRecord
	for _, r := range l.records {
		if r.Seq > seq {
			out = append(out, r)
		}
	}
	return out
}

// All returns every record in chronological (append) order.
func (l *FaultLog) All() []FaultRecord {
	out := make([]FaultRecord, len(l.records))
	copy(out, l.records)
	return out
}

// NextSeq reports the sequence the next append will receive.
func (l *FaultLog) NextSeq() int64 { return l.nextSeq }

// Len returns the number of stored records.
func (l *FaultLog) Len() int { return len(l.records) }
