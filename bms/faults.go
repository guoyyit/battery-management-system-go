package bms

// FaultLog is an append-only, pageable, time-ordered fault history.
// Records keep the monotonic IDs assigned by Protection; the log never
// rewrites or drops a committed record.
type FaultLog struct {
	records []FaultRecord
}

// NewFaultLog constructs an empty fault log.
func NewFaultLog() *FaultLog { return &FaultLog{} }

// Append stores one record. The record's ID is preserved; records are kept in
// append order (monotonic IDs, so oldest-first chronologically).
func (l *FaultLog) Append(r FaultRecord) FaultRecord {
	l.records = append(l.records, r)
	return r
}

// AppendAll stores records in bulk, used during crash recovery.
func (l *FaultLog) AppendAll(rs []FaultRecord) {
	l.records = append(l.records, rs...)
}

// Page returns one page ordered oldest-first. Pages are zero-based; a
// non-positive pageSize defaults to 50.
func (l *FaultLog) Page(page, pageSize int) FaultPage {
	if pageSize <= 0 {
		pageSize = 50
	}
	if page < 0 {
		page = 0
	}
	total := len(l.records)
	start := page * pageSize
	if start > total {
		start = total
	}
	end := start + pageSize
	if end > total {
		end = total
	}
	pageRecords := make([]FaultRecord, end-start)
	copy(pageRecords, l.records[start:end])
	return FaultPage{
		Records:  pageRecords,
		Total:    total,
		Page:     page,
		PageSize: pageSize,
	}
}

// All returns all records oldest-first (copy), mainly for persistence.
func (l *FaultLog) All() []FaultRecord {
	out := make([]FaultRecord, len(l.records))
	copy(out, l.records)
	return out
}

// Len reports the number of stored records.
func (l *FaultLog) Len() int { return len(l.records) }

// LastID returns the ID of the last stored record, or 0 if empty.
func (l *FaultLog) LastID() int64 {
	if len(l.records) == 0 {
		return 0
	}
	return l.records[len(l.records)-1].ID
}
