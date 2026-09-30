package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestCPUBudgetDefaultsToTheMachinesCores: the engine ships to machines it cannot know, so it
// holds no core count of its own. The default has to come from the machine it is running on —
// a constant here would be one deployment's hardware written into everyone's engine.
func TestCPUBudgetDefaultsToTheMachinesCores(t *testing.T) {
	t.Setenv("SILPHUU_CPUS", "")
	if got, want := cpuBudget(), runtime.NumCPU(); got != want {
		t.Errorf("cpuBudget() = %d with no override, want the machine's %d cores", got, want)
	}
}

// TestCPUBudgetHonoursTheOverride: a deployment that shares its box names the cores it is
// actually allowed. The override is the only place that number can live, so it has to be read,
// and a value that cannot be a core count must fall back rather than reach the gate — a gate
// built from 0 or a negative number admits nothing, which would stall every subset forever.
func TestCPUBudgetHonoursTheOverride(t *testing.T) {
	t.Setenv("SILPHUU_CPUS", "3")
	if got := cpuBudget(); got != 3 {
		t.Errorf("cpuBudget() = %d with SILPHUU_CPUS=3", got)
	}

	cores := runtime.NumCPU()
	for _, raw := range []string{"0", "-1", "two", "2.5", " "} {
		t.Setenv("SILPHUU_CPUS", raw)
		if got := cpuBudget(); got != cores {
			t.Errorf("cpuBudget() = %d with SILPHUU_CPUS=%q, want the machine's %d cores", got, raw, cores)
		}
	}
}

// TestCPUBudgetIsTheOnlySourceOfTheGateSize pins where the gate's capacity comes from.
//
// The engine's whole reason for having cpuBudget is that no core count belongs in its source,
// and a gate sized by a literal would put one back without looking wrong at the call site.
// So this reads the source: cpuSlots must be newCPUGate(cpuBudget()), and nothing may pass a
// literal to newCPUGate.
func TestCPUBudgetIsTheOnlySourceOfTheGateSize(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}

	var wired bool
	var literals []string
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(token.NewFileSet(), file, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", file, err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			id, ok := call.Fun.(*ast.Ident)
			if !ok || id.Name != "newCPUGate" || len(call.Args) != 1 {
				return true
			}
			arg := call.Args[0]
			if lit, ok := arg.(*ast.BasicLit); ok {
				literals = append(literals, file+": newCPUGate("+lit.Value+")")
				return true
			}
			inner, ok := arg.(*ast.CallExpr)
			if !ok {
				return true
			}
			if innerID, ok := inner.Fun.(*ast.Ident); ok && innerID.Name == "cpuBudget" {
				wired = true
			}
			return true
		})
	}

	if len(literals) > 0 {
		t.Errorf("a gate is sized by a literal: %v — a core count in the engine's source is one "+
			"deployment's hardware; it belongs in SILPHUU_CPUS", literals)
	}
	if !wired {
		t.Error("no newCPUGate(cpuBudget()) in the engine: the gate's capacity no longer comes " +
			"from the machine or from SILPHUU_CPUS")
	}
}

// TestCPUGateBoundsTheProcessWideInstance: the ceiling only holds if every fan-out contends
// for the same slots. cpuSlots is a package-level instance for that reason, so this checks the
// instance the engine uses is a real gate of the budgeted size — filling it has to stop the
// next holder, and releasing a slot has to let it through.
func TestCPUGateBoundsTheProcessWideInstance(t *testing.T) {
	budget := cpuBudget()
	if got := cap(cpuSlots.slots); got != budget {
		t.Fatalf("cpuSlots admits %d, the budget is %d", got, budget)
	}

	// Drain afterwards whatever happens: it is package state, and a slot left held here
	// would stall the next test that runs a CPU-bound fan-out.
	t.Cleanup(func() {
		for {
			select {
			case <-cpuSlots.slots:
			default:
				return
			}
		}
	})

	for i := 0; i < budget; i++ {
		cpuSlots.acquire()
	}

	held := make(chan struct{})
	go func() {
		cpuSlots.acquire()
		close(held)
		cpuSlots.release()
	}()

	select {
	case <-held:
		t.Fatal("a holder was admitted while every slot was taken")
	case <-time.After(50 * time.Millisecond):
	}

	cpuSlots.release()

	select {
	case <-held:
	case <-time.After(2 * time.Second):
		t.Fatal("a holder was not admitted after a slot was released")
	}
}
