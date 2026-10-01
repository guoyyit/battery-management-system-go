package bms

import (
	"sync"
	"time"
)

// BMS ties sampling, balancing, protection, queries and persistence together.
// One mutex guards every mutation of subsystem state and every externally
// visible snapshot, so queries can never observe a half-applied cycle — in
// particular, when a fault trips, current is forced to zero before any coulomb
// integration occurs in that same cycle.
type BMS struct {
	cfg     Config
	store   *Store
	sim     *Simulator
	soc     *SOCEstimator
	bal     *Balancer
	limiter *CurrentLimiter
	prot    *Protection
	faults  *FaultLog

	mu          sync.RWMutex
	requestedA  float64
	last        PackSnapshot
	running     bool
	stopCh      chan struct{}
	doneCh      chan struct{}
	lastSave    time.Time
	bootOCVDone bool
}

// New constructs a BMS, restoring persisted state when present.
func New(cfg Config) (*BMS, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	b := &BMS{
		cfg:     cfg,
		store:   NewStore(cfg.StorePath),
		bal:     NewBalancer(cfg),
		limiter: NewCurrentLimiter(cfg),
		prot:    NewProtection(cfg),
		faults:  NewFaultLog(),
		stopCh:  make(chan struct{}),
		doneCh:  make(chan struct{}),
	}

	st, ok, err := b.store.Load()
	if err != nil {
		return nil, err
	}
	var bootSimSOC []float64
	var bootTemp float64
	if ok {
		b.restore(st)
		b.bootOCVDone = true
		bootSimSOC = st.SimSOC
		bootTemp = st.SimTempC
	} else {
		b.soc = NewSOCEstimator(cfg, 0.5)
		b.last = b.initialSnapshot()
	}
	b.sim = NewSimulator(cfg, b.soc.SOC())
	if bootSimSOC != nil {
		b.sim.RestoreTrueSOC(bootSimSOC)
		b.sim.tempC = bootTemp
	}

	return b, nil
}

func (b *BMS) initialSnapshot() PackSnapshot {
	cells := make([]CellState, b.cfg.CellCount)
	for i := range cells {
		cells[i] = CellState{
			Index:    i,
			VoltageV: b.cfg.OCVTable.Voltage(b.soc.SOC()),
			SOC:      b.soc.SOC(),
		}
	}
	return PackSnapshot{
		Time:    time.Now(),
		Cells:   cells,
		PackSOC: b.soc.SOC(),
		TempC:   25,
	}
}

func (b *BMS) restore(st PersistedState) {
	b.soc = NewSOCEstimator(b.cfg, st.PackSOC)
	b.soc.Restore(st.CellSOC, st.CumulativeAh)
	b.bal.Restore(st.Switching, st.BalancedAh)
	b.prot.Restore(st.FaultLatched, st.ActiveFaults, st.NextFaultID)
	b.faults.AppendAll(st.Faults)
	b.last = PackSnapshot{
		PackSOC:      st.PackSOC,
		FaultLatched: st.FaultLatched,
		ActiveFaults: st.ActiveFaults,
		CumulativeAh: meanF(st.CumulativeAh),
	}
}

// Start launches the sampling loop at the configured interval.
func (b *BMS) Start() {
	b.mu.Lock()
	if b.running {
		b.mu.Unlock()
		return
	}
	b.running = true
	b.mu.Unlock()

	go b.loop()
}

func (b *BMS) loop() {
	defer close(b.doneCh)
	ticker := time.NewTicker(b.cfg.SampleInterval)
	defer ticker.Stop()
	for {
		select {
		case <-b.stopCh:
			return
		case t := <-ticker.C:
			b.Step(t)
		}
	}
}

// Stop cleanly shuts the loop down and persists state.
func (b *BMS) Stop() {
	b.mu.Lock()
	if !b.running {
		b.mu.Unlock()
		return
	}
	b.running = false
	close(b.stopCh)
	b.mu.Unlock()
	<-b.doneCh

	b.mu.RLock()
	st := b.persistedStateLocked()
	b.mu.RUnlock()
	_ = b.store.Save(st)
}

// Step runs one synchronous sample -> limit -> protect -> (integrate) ->
// balance ->
// protect cycle, then persists if an event occurred or the interval elapsed.
func (b *BMS) Step(now time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()

	wasLatched := b.prot.Latched()
	preLatchedFaults := b.prot.ActiveFaults()

	// 1. Dynamic current limit. While latched, no current may flow.
	var reqA float64
	if !wasLatched {
		reqA = b.requestedA
	}
	socNow := b.soc.SOC()
	tempNow := b.packTempLocked()
	minV, maxV := b.cellExtremesLocked()
	lr := b.limiter.Apply(reqA, socNow, tempNow, minV, maxV)
	if wasLatched {
		lr = LimitResult{Requested: b.requestedA, Applied: 0, LimitA: 0,
			Reason: "fault_latched"}
	}

	// 2. Drive the battery model with the applied current and sample it.
	b.sim.SetRequest(lr.Applied)
	b.sim.SetBalancing(b.bal.Switching())
	m := b.sim.Sample(now)

	// 3. Protection FIRST: evaluate against the fresh sample. A trip in this
	// cycle means the frame's current is not coulomb-counted.
	preSnap := b.buildSnapshotLocked(m, lr, now)
	events := b.prot.Evaluate(preSnap)
	if b.prot.Latched() {
		// Force the frame to zero current; the contactor opens immediately.
		m.CurrentA = 0
		lr.Applied = 0
		lr.Reason = "fault_latched"
		b.sim.SetRequest(0)
	} else {
		// 4. Only integrate when the contactor is closed this whole frame.
		if !b.bootOCVDone && absF(lr.Applied) <= restCurrentThresholdA {
			// After power-up while standing, converge via OCV immediately.
			b.soc.ForceOCVCorrect(m)
			b.bootOCVDone = true
		} else {
			b.soc.Observe(m)
			b.bootOCVDone = true
		}
	}

	// 5. Balancing decision using valid (shunt-compensated) voltages.
	b.bal.Decide(m, b.soc.CellSOC())

	// 6. Commit the self-consistent visible snapshot and fault records.
	b.last = b.buildSnapshotLocked(m, lr, now)
	var saveNow bool
	for _, ev := range events {
		// After a crash the protection subsystem rebuilds its active set from
		// the next frame; suppress re-"triggered" records for fault types that
		// were already latched before the crash, so history never duplicates.
		if ev.Record.Action == FaultTriggered && wasLatched &&
			containsFault(preLatchedFaults, ev.Record.Type) {
			continue
		}
		b.faults.Append(ev.Record)
		saveNow = true
	}

	if saveNow || now.Sub(b.lastSave) >= time.Second {
		b.lastSave = now
		_ = b.store.Save(b.persistedStateLocked())
	}
}

func containsFault(set []FaultType, want FaultType) bool {
	for _, f := range set {
		if f == want {
			return true
		}
	}
	return false
}

func (b *BMS) buildSnapshotLocked(m Measurement, lr LimitResult, now time.Time) PackSnapshot {
	eff := b.bal.EffectiveVoltages(m)
	cells := make([]CellState, b.cfg.CellCount)
	switching := b.bal.Switching()
	balanced := b.bal.BalancedAh()
	socs := b.soc.CellSOC()
	maxTemp := 0.0
	for i := range cells {
		cells[i] = CellState{
			Index:      i,
			VoltageV:   eff[i],
			SOC:        socs[i],
			Balancing:  switching[i],
			BalancedAh: balanced[i],
		}
		if m.TempsC[i] > maxTemp {
			maxTemp = m.TempsC[i]
		}
	}
	return PackSnapshot{
		Time:           now,
		Cells:          cells,
		PackSOC:        meanF(socs),
		CurrentA:       m.CurrentA,
		AppliedCurrent: lr.Applied,
		LimitA:         lr.LimitA,
		LimitReason:    lr.Reason,
		TempC:          maxTemp,
		FaultLatched:   b.prot.Latched(),
		ActiveFaults:   b.prot.ActiveFaults(),
		CumulativeAh:   meanF(b.soc.IntegratedAh()),
	}
}

func (b *BMS) packTempLocked() float64 {
	if len(b.last.Cells) == 0 {
		return 25
	}
	return b.last.TempC
}

func (b *BMS) cellExtremesLocked() (float64, float64) {
	if len(b.last.Cells) == 0 {
		return b.cfg.OCVTable.Voltage(b.soc.SOC()),
			b.cfg.OCVTable.Voltage(b.soc.SOC())
	}
	minV, maxV := b.last.Cells[0].VoltageV, b.last.Cells[0].VoltageV
	for _, c := range b.last.Cells[1:] {
		if c.VoltageV < minV {
			minV = c.VoltageV
		}
		if c.VoltageV > maxV {
			maxV = c.VoltageV
		}
	}
	return minV, maxV
}

func (b *BMS) persistedStateLocked() PersistedState {
	return PersistedState{
		PackSOC:      b.soc.SOC(),
		CumulativeAh: b.soc.IntegratedAh(),
		CellSOC:      b.soc.CellSOC(),
		SimSOC:       b.sim.TrueSOC(),
		SimTempC:     b.sim.tempC,
		BalancedAh:   b.bal.BalancedAh(),
		Switching:    b.bal.Switching(),
		FaultLatched: b.prot.Latched(),
		ActiveFaults: b.prot.ActiveFaults(),
		Faults:       b.faults.All(),
		NextFaultID:  b.faults.LastID(),
	}
}

// Snapshot returns a self-consistent copy of the current state.
func (b *BMS) Snapshot() PackSnapshot {
	b.mu.RLock()
	defer b.mu.RUnlock()
	out := b.last
	out.Cells = append([]CellState(nil), b.last.Cells...)
	out.ActiveFaults = append([]FaultType(nil), b.last.ActiveFaults...)
	return out
}

// RequestCurrent sets the desired pack current (A, + charge / - discharge).
func (b *BMS) RequestCurrent(a float64) {
	b.mu.Lock()
	b.requestedA = a
	b.mu.Unlock()
}

// ClearFaults explicitly clears a latched fault when conditions are safe.
func (b *BMS) ClearFaults() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	events := b.prot.ClearFaults(time.Now())
	if events == nil {
		return false
	}
	for _, ev := range events {
		b.faults.Append(ev.Record)
	}
	_ = b.store.Save(b.persistedStateLocked())
	return true
}

// FaultHistory returns one page of fault records, oldest first.
func (b *BMS) FaultHistory(page, pageSize int) FaultPage {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.faults.Page(page, pageSize)
}

// SetAmbient adjusts the simulator ambient temperature.
func (b *BMS) SetAmbient(celsius float64) {
	b.mu.Lock()
	b.sim.SetAmbient(celsius)
	b.mu.Unlock()
}

// InjectShortCircuit toggles simulated short-circuit conditions (test hook).
func (b *BMS) InjectShortCircuit(on bool) {
	b.mu.Lock()
	b.sim.InjectShortCircuit(on)
	b.mu.Unlock()
}
