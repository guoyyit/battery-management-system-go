// Command bmsim is a long-running, fully local simulation driver for the BMS.
//
// It replays a deterministic 24 h mission profile (discharge/rest/charge plus
// a short-circuit and an over-temperature episode), persisting state every few
// seconds. Restarting the binary resumes from the last durable snapshot,
// demonstrating crash recovery of SOC, balancing progress and fault latches.
package main

import (
	"flag"
	"log"
	"math"
	"time"

	"github.com/guoyyit/battery-management-system-go/bms"
)

func main() {
	stateDir := flag.String("state", "bms-state", "state directory (created if absent)")
	duration := flag.Duration("duration", 24*time.Hour, "simulated mission duration")
	seed := flag.Int64("seed", 42, "deterministic simulation seed")
	flag.Parse()

	cfg := bms.DefaultConfig()
	cfg.Model.Seed = *seed
	cfg.StateDir = *stateDir
	// Persist fast enough that an OS kill between ticks loses little energy.
	cfg.PersistEvery = 10 * time.Second

	t0 := time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC)
	sys, err := bms.New(cfg, t0)
	if err != nil {
		log.Fatalf("init: %v", err)
	}

	st := sys.Status()
	log.Printf("recovered: contactorOpen=%v faults=%d history=%d",
		st.ContactorOpen, len(st.Faults), len(sys.FaultPage(0, 0)))

	step := time.Second
	const reportEvery = 30 * time.Minute
	nextReport := step
	var t time.Duration
	for ; t < *duration; t += step {
		in := profile(t)
		st, err = sys.Step(step, in)
		if err != nil {
			log.Fatalf("step @%s: %v", t, err)
		}
		if t >= nextReport {
			report(t, st)
			nextReport += reportEvery
		}
	}

	if err := sys.Close(); err != nil {
		log.Fatalf("close: %v", err)
	}
	log.Printf("mission complete, state saved to %s", *stateDir)
}

// profile is the deterministic commanded current / ambient temperature at
// simulated time t. Negative current charges.
func profile(t time.Duration) bms.Inputs {
	switch {
	case t < 4*time.Hour: // morning discharge
		return bms.Inputs{Current: 40, AmbientTemp: 25}
	case t < 14*time.Hour: // long rest -> OCV correction + balancing window
		return bms.Inputs{Current: 0, AmbientTemp: 25}
	case t < 18*time.Hour-time.Second: // afternoon charge
		return bms.Inputs{Current: -60, AmbientTemp: 30}
	case t < 18*time.Hour: // one-second short circuit
		return bms.Inputs{Current: -350, AmbientTemp: 30}
	case t < 20*time.Hour: // hot soak after a blocked cooling duct
		return bms.Inputs{Current: 90, AmbientTemp: 58}
	default: // overnight rest
		return bms.Inputs{Current: 0, AmbientTemp: 20}
	}
}

func report(t time.Duration, st bms.Status) {
	min, max := math.Inf(1), math.Inf(-1)
	for _, s := range st.SOC {
		if s < min {
			min = s
		}
		if s > max {
			max = s
		}
	}
	balancing := 0
	balAh := 0.0
	for i, on := range st.Balance.Active {
		if on {
			balancing++
		}
		balAh += st.Balance.ChargeAh[i]
	}
	log.Printf("t=%6s SOC[%.3f..%.3f] applied=%7.1fA contactor=%v faults=%d balancing=%d/%d balAh=%.4f corr=%v",
		t.Truncate(time.Second), min, max,
		st.Limit.AppliedCurrentA, st.ContactorOpen, len(st.Faults),
		balancing, len(st.Balance.Active), balAh, st.SOCCorrected)
}
