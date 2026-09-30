package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestSubsetFanOutStaysWithinTheGate pins that the subset fan-out runs no more tasks at once
// than the gate admits.
//
// The subsetter is the engine's only CPU-bound fan-out, and it hands every task to single-
// threaded children (hb-subset then woff2_compress), so the number of simultaneous tasks is
// the number of cores the engine takes. Without the gate the four pipelines start together
// and put their whole task list on the machine at once; nothing else in the tree would notice.
//
// Both directions are asserted. A ceiling that is too high is the failure being fixed; one
// that is too low would leave cores idle for the whole run, and a test that only checked "not
// more than N" would pass with the fan-out serialised to a single slot.
//
// The gate is the test's own instance rather than cpuSlots, so a pipeline another test left
// running cannot hold one of the slots this test is counting.
func TestSubsetFanOutStaysWithinTheGate(t *testing.T) {
	budget := cpuBudget()
	taskCount := budget*2 + 1

	tasks := make([]fontSubsetTask, taskCount)
	for i := range tasks {
		tasks[i] = fontSubsetTask{Src: fmt.Sprintf("src-%d", i), Out: fmt.Sprintf("out-%d", i)}
	}

	var mu sync.Mutex
	live, peak := 0, 0

	results := runSubsetTasks(newCPUGate(budget), tasks, func(t fontSubsetTask) (taskResult, error) {
		mu.Lock()
		live++
		if live > peak {
			peak = live
		}
		mu.Unlock()

		// Hold the slot long enough for every other goroutine to reach the gate, so the
		// peak reflects the ceiling rather than how fast the runner returns.
		time.Sleep(10 * time.Millisecond)

		mu.Lock()
		live--
		mu.Unlock()

		return taskResult{Out: t.Out}, nil
	})

	if len(results) != taskCount {
		t.Fatalf("the fan-out returned %d results for %d tasks", len(results), taskCount)
	}
	// Results are positional: the caller reads results[i] against tasks[i] to report a
	// failure, so a runner that finished first must not move up the slice.
	for i, res := range results {
		if want := fmt.Sprintf("out-%d", i); res.Out != want {
			t.Errorf("results[%d].Out = %q, want %q — results are not in input order", i, res.Out, want)
		}
		if res.Err != nil {
			t.Errorf("results[%d].Err = %v for a runner that succeeded", i, res.Err)
		}
	}
	// A failing runner must carry its error out through the same slice: that is how
	// executeSubsetTasks counts failures and decides whether to report the batch as failed.
	failed := runSubsetTasks(newCPUGate(budget), []fontSubsetTask{{Out: "boom"}},
		func(fontSubsetTask) (taskResult, error) { return taskResult{}, fmt.Errorf("nope") })
	if failed[0].Err == nil {
		t.Error("a failing runner's error was dropped, so a failed batch would report success")
	}

	if peak > budget {
		t.Errorf("%d subset tasks ran at once; the gate admits %d", peak, budget)
	}
	if peak < budget {
		t.Errorf("only %d subset task ran at once, but the gate admits %d: the fan-out is "+
			"serialising past the ceiling, leaving %d core idle", peak, budget, budget-peak)
	}
}

// TestSubsetWorkGoesThroughTheGate: the ceiling is only worth anything if every task that
// spawns a subsetter passes through it. A pipeline that called executeSingleSubsetTask itself
// would run unbounded, and nothing about the call site would look wrong. So this pins the
// wiring rather than the arithmetic: one call site, in executeSubsetTasks, driven by cpuSlots.
func TestSubsetWorkGoesThroughTheGate(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}

	callSites := map[string][]string{}
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(token.NewFileSet(), file, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", file, err)
		}
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				if id, ok := call.Fun.(*ast.Ident); ok && id.Name == "executeSingleSubsetTask" {
					callSites[fn.Name.Name] = append(callSites[fn.Name.Name], file)
				}
				return true
			})
		}
	}

	if len(callSites) != 1 {
		t.Fatalf("executeSingleSubsetTask is called from %d functions, want exactly 1 — every "+
			"other one runs subsets without passing through the gate: %v", len(callSites), callSites)
	}
	if _, ok := callSites["executeSubsetTasks"]; !ok {
		t.Fatalf("executeSingleSubsetTask is no longer called from executeSubsetTasks: %v", callSites)
	}

	// And inside that function the fan-out must be the process-wide gate's, not a private one.
	f, err := parser.ParseFile(token.NewFileSet(), "font_subset.go", nil, 0)
	if err != nil {
		t.Fatalf("parse font_subset.go: %v", err)
	}
	var onSharedGate bool
	ast.Inspect(f, func(n ast.Node) bool {
		fn, ok := n.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "executeSubsetTasks" || fn.Body == nil {
			return true
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			id, ok := call.Fun.(*ast.Ident)
			if !ok || id.Name != "runSubsetTasks" || len(call.Args) == 0 {
				return true
			}
			if first, ok := call.Args[0].(*ast.Ident); ok && first.Name == "cpuSlots" {
				onSharedGate = true
			}
			return true
		})
		return true
	})
	if !onSharedGate {
		t.Error("executeSubsetTasks no longer fans out through cpuSlots, so the process-wide " +
			"budget does not bound it")
	}
}
