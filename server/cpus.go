package main

import (
	"log"
	"os"
	"runtime"
	"strconv"
)

// The engine's CPU-bound fan-outs hand their work to external commands. The ones it runs in
// parallel are single-threaded each — hb-subset and woff2_compress are two such children run
// one after the other, so one subset task keeps one core busy and N tasks want N cores.
// cpuBudget is that N, and every fan-out takes its width from here rather than keeping a
// number of its own.
//
// The default is the machine's own core count. The engine ships to machines it cannot know,
// so it holds no core count of its own; a deployment that shares the box — pinning other
// services to particular cores — names the cores it is actually allowed in SILPHUU_CPUS.
// A value that is not a positive integer falls back to the machine's count with a warning
// rather than serialising the engine.
func cpuBudget() int {
	cores := runtime.NumCPU()
	raw := os.Getenv("SILPHUU_CPUS")
	if raw == "" {
		return cores
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 {
		log.Printf("SILPHUU_CPUS=%q is not a positive integer; using the machine's %d cores", raw, cores)
		return cores
	}
	return n
}

// cpuGate admits cpuBudget holders at a time.
//
// It bounds running children, not started goroutines: a holder waits for a slot before it
// spawns anything, so a fan-out of a thousand tasks still puts at most cpuBudget commands on
// the machine. A child that spawns its own threads — avifenc's -j — chooses its own width and
// is not counted here.
type cpuGate struct {
	slots chan struct{}
}

// newCPUGate returns a gate admitting n holders at a time.
func newCPUGate(n int) *cpuGate {
	return &cpuGate{slots: make(chan struct{}, n)}
}

// acquire takes a slot, blocking until one is free.
func (g *cpuGate) acquire() {
	g.slots <- struct{}{}
}

// release returns the slot acquire took.
func (g *cpuGate) release() {
	<-g.slots
}

// cpuSlots is the process-wide gate. Every CPU-bound fan-out draws on it, so two features that
// both spawn children share the machine's cores instead of each taking cpuBudget of them.
var cpuSlots = newCPUGate(cpuBudget())
