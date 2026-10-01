package bms

import (
	"fmt"
	"sync"
	"time"
)

// Status is the self-consistent observable state of one moment.
type Status struct {
	Time time.Time

	Measurement  Measurement
	TrueSOC      []float64
	SOC          []float64
	SOCCorrected bool

	Balance BalanceStatus
	Limit   LimitResult

	Faults        []ActiveFault
	ContactorOpen bool
}

// BMS is the concurrent-safe battery management controller.
//
// All mutable state is guarded by one mutex, so every observation is a
// snapshot taken at a single decision point: sampling, balancing, protection
// and queries can never interleave into an inconsistent intermediate state.
type BMS struct {
	mu  sync.RWMutex
	cfg *Config

	now       time.Time
	model     *Model
	estimator *Estimator
	balancer  *Balancer
	limiter   *CurrentLimiter
	protect   *Protection
	faults    *FaultLog
	store     Store

	lastMeas   Measurement
	lastStatus Status

	sincePersist time.Duration
}

// New constructs a BMS and restores prior state from cfg.StateDir when present.
func New(cfg *Config, now time.Time) (*BMS, error) {
	if cfg == nil {
		cfg = DefaultConfig()
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	n := cfg.Model.SeriesCount
	model := NewModel(cfg.Model, now)

	var store Store
	if cfg.StateDir != "" {
		fs, err := NewFileStore(cfg.StateDir)
		if err != nil {
			return nil, err
		}
		store = fs
	}

	var (
		state *PersistedState
		err   error
	)
	if store != nil {
		state, err = store.Load()
		if err != nil {
			return nil, fmt.Errorf("bms: load state: %w", err)
		}
	}

	var initialSOC []float64
	var active []bool
	var chargeAh []float64
	var latched []ActiveFault
	var records []FaultRecord
	var nextSeq int64
	if state != nil {
		initialSOC = state.SOC
		active = state.BalActive
		chargeAh = state.BalChargeAh
		latched = state.TrippedFaults
		records = state.FaultRecords
		nextSeq = state.NextFaultSeq
		// The simulation restarts with no physical history; resync the model
		// to the recovered estimate so ground truth stays continuous.
		model.Reset(initialSOC, now)
	} else {
		initialSOC = model.TrueSOC()
	}

	b := &BMS{
		cfg:       cfg,
		now:       now,
		model:     model,
		estimator: NewEstimator(cfg.SOC, initialSOC, n),
		balancer:  NewBalancer(cfg.Balance, n, active, chargeAh),
		limiter:   NewCurrentLimiter(cfg.Limits),
		protect:   NewProtection(cfg.Protection, latched),
		faults:    NewFaultLog(records, nextSeq),
		store:     store,
	}

	// Prime a rest sample so limit/protection decisions have voltages.
	b.lastMeas = model.Step(0, Inputs{AmbientTemp: 25}, now)
	b.lastStatus = b.snapshot(now, false, LimitResult{}, b.lastMeas)
	return b, nil
}

// Step advances the whole system by dt with the given commanded inputs.
// Requested current is clamped by the limiter and zeroed on protection.
func (b *BMS) Step(dt time.Duration, in Inputs) (Status, error) {
	if dt <= 0 {
		return Status{}, fmt.Errorf("bms: step duration must be positive")
	}
	if dt > b.cfg.MaxStep {
		return Status{}, fmt.Errorf("bms: step %s exceeds max %s", dt, b.cfg.MaxStep)
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	now := b.now.Add(dt)

	// 1. Protection first. Over-current/short-circuit are fault conditions on
	//    the *requested* current: waiting for the limiter would make those
	//    trips unreachable. Voltage/temperature come from the previous sample.
	reqMeas := b.lastMeas
	reqMeas.CurrentSensed = in.Current
	newTrips, open := b.protect.Evaluate(dt, reqMeas)
	for _, t := range newTrips {
		b.faults.Append(FaultRecord{
			At:          t.At,
			Type:        t.Type,
			Cell:        t.Cell,
			Voltage:     t.Voltage,
			Temperature: t.Temperature,
			Current:     t.Current,
			Tripped:     true,
		})
	}

	var lim LimitResult
	applied := in.Current
	if open {
		lim = LimitResult{
			ChargeLimitA:    b.cfg.Limits.MaxChargeA,
			DischargeLimitA: b.cfg.Limits.MaxDischargeA,
			AppliedCurrentA: 0,
			Limited:         in.Current != 0,
			Reasons:         []string{"contactor open: protection latched"},
		}
		applied = 0
	} else {
		lim = b.limiter.Apply(in.Current, b.lastMeas.Temperature,
			b.estimator.SOC(), b.lastMeas.CellVoltage)
		applied = lim.AppliedCurrentA
	}

	// 2. Physics + sampling with the contactor-controlled actual current.
	// If the contactor just opened, force bypass switches off *before* the
	// physics step so no balancing current flows during the fault tick.
	var bal BalanceStatus
	if open {
		bal = b.balancer.Update(0, b.lastMeas, b.estimator.SOC(), true)
	} else {
		bal = b.balancer.Status()
	}
	b.model.setBypass(bal.Active, bal.CurrentA)
	meas := b.model.Step(dt, Inputs{Current: applied, AmbientTemp: in.AmbientTemp}, now)

	// 3. Balancing decision and SOC integration on the new sample.
	bal = b.balancer.Update(dt, meas, b.estimator.SOC(), open)
	estInput := meas
	if open {
		// Contactor open: no current is metered into SOC while latched.
		estInput.CurrentSensed = 0
	}
	soc, corrected := b.estimator.Update(dt, estInput, bal.Active, bal.CurrentA)

	st := b.snapshot(now, corrected, lim, meas)
	st.Balance = bal
	st.SOC = soc

	b.lastMeas = meas
	b.lastStatus = st
	b.now = now

	// 4. Persistence: immediately on latch changes, otherwise periodically.
	b.sincePersist += dt
	force := len(newTrips) > 0
	if force || (b.cfg.PersistEvery > 0 && b.sincePersist >= b.cfg.PersistEvery) {
		if err := b.persistLocked(now); err != nil {
			return st, err
		}
		b.sincePersist = 0
	}
	return st, nil
}

func (b *BMS) snapshot(now time.Time, corrected bool, lim LimitResult, m Measurement) Status {
	return Status{
		Time:          now,
		Measurement:   cloneMeas(m),
		TrueSOC:       b.model.TrueSOC(),
		SOC:           b.estimator.SOC(),
		SOCCorrected:  corrected,
		Balance:       b.balancer.Status(),
		Limit:         lim,
		Faults:        b.protect.Tripped(),
		ContactorOpen: b.protect.IsTripped(),
	}
}

// Status returns a snapshot of the current self-consistent state.
func (b *BMS) Status() Status {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return cloneStatus(b.lastStatus)
}

// ClearFaults explicitly unlatches faults whose clear (hysteresis) conditions
// hold against the latest sample. Nothing ever auto-clears.
func (b *BMS) ClearFaults(now time.Time) ([]FaultType, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	cleared := b.protect.Clear(b.lastMeas)
	for _, ft := range cleared {
		b.faults.Append(FaultRecord{
			At:          now,
			Type:        ft,
			Voltage:     worstV(b.lastMeas),
			Temperature: worstT(b.lastMeas),
			Current:     b.lastMeas.CurrentSensed,
			Tripped:     false,
		})
	}
	b.lastStatus.Faults = b.protect.Tripped()
	b.lastStatus.ContactorOpen = b.protect.IsTripped()
	if len(cleared) > 0 {
		if err := b.persistLocked(now); err != nil {
			return cleared, err
		}
		b.sincePersist = 0
	}
	return cleared, nil
}

// FaultPage returns a chronological page of fault history.
func (b *BMS) FaultPage(offset, limit int) []FaultRecord {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.faults.Page(offset, limit)
}

// Persist forces an immediate durable snapshot.
func (b *BMS) Persist() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.persistLocked(b.now)
}

func (b *BMS) persistLocked(now time.Time) error {
	if b.store == nil {
		return nil
	}
	st := b.lastStatus
	return b.store.Save(&PersistedState{
		SavedAt:       now,
		SOC:           st.SOC,
		BalActive:     st.Balance.Active,
		BalChargeAh:   st.Balance.ChargeAh,
		TrippedFaults: st.Faults,
		FaultRecords:  b.faults.All(),
		NextFaultSeq:  b.faults.NextSeq(),
	})
}

// Close flushes state to disk.
func (b *BMS) Close() error { return b.Persist() }

func cloneMeas(m Measurement) Measurement {
	out := m
	out.CellVoltage = append([]float64(nil), m.CellVoltage...)
	out.Temperature = append([]float64(nil), m.Temperature...)
	return out
}

func cloneStatus(s Status) Status {
	out := s
	out.Measurement = cloneMeas(s.Measurement)
	out.TrueSOC = append([]float64(nil), s.TrueSOC...)
	out.SOC = append([]float64(nil), s.SOC...)
	out.Balance.Active = append([]bool(nil), s.Balance.Active...)
	out.Balance.CurrentA = append([]float64(nil), s.Balance.CurrentA...)
	out.Balance.ChargeAh = append([]float64(nil), s.Balance.ChargeAh...)
	out.Limit.Reasons = append([]string(nil), s.Limit.Reasons...)
	out.Faults = append([]ActiveFault(nil), s.Faults...)
	return out
}
