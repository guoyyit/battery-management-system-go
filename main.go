package main

import (
	"fmt"
	"time"

	"github.com/guoyyit/battery-management-system-go/bms"
)

func main() {
	cfg := bms.DefaultConfig(4, 2.5)
	cfg.StorePath = "bms_state.json"

	sys, err := bms.New(cfg)
	if err != nil {
		panic(err)
	}

	sys.Start()
	sys.RequestCurrent(2.0)
	time.Sleep(2 * time.Second)

	snap := sys.Snapshot()
	fmt.Printf("SOC=%.3f current=%.3fA limited=%.3fA reason=%q faults=%v\n",
		snap.PackSOC, snap.CurrentA, snap.LimitA, snap.LimitReason, snap.ActiveFaults)

	sys.Stop()
	fmt.Println("stopped")
}
