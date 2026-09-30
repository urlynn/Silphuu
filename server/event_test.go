package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestNodeNameIsTheHostNameUnlessOverridden: the identity has to be unique across the cluster
// and stable across restarts, and that is all it has to be, so a node is distinguishable
// without being configured. It is not an address, and nothing derives one from it.
//
// An unreadable host name fails rather than skips: a guard that cannot reach its subject
// proves nothing about the default it is supposed to be checking.
func TestNodeNameIsTheHostNameUnlessOverridden(t *testing.T) {
	host, err := os.Hostname()
	if err != nil || host == "" {
		t.Fatalf("os.Hostname() = %q, %v; the default cannot be checked", host, err)
	}

	t.Setenv("NODE_NAME", "")
	if got := nodeName(); got != host {
		t.Errorf("nodeName() = %q, want the host name %q", got, host)
	}

	t.Setenv("NODE_NAME", "node-a")
	if got := nodeName(); got != "node-a" {
		t.Errorf("nodeName() = %q, want the override", got)
	}
}

// boundedReader serves at most limit bytes and then fails. A scan that reads further than its
// caller asked for cannot pass through it.
type boundedReader struct {
	data  []byte
	pos   int
	limit int
}

func (r *boundedReader) Read(p []byte) (int, error) {
	if r.pos >= r.limit {
		return 0, errors.New("read past the bound")
	}
	end := r.pos + len(p)
	if end > r.limit {
		end = r.limit
	}
	if end > len(r.data) {
		end = len(r.data)
	}
	if end <= r.pos {
		return 0, errors.New("read past the data")
	}
	n := copy(p, r.data[r.pos:end])
	r.pos += n
	return n, nil
}

// TestScanEventStreamStopsAtTheLimit: a caller that wants a prefix of the log must not pay for
// the whole of it. The reader fails once the scan goes past what the two events it visits
// need, so an implementation that keeps scanning to the end reports the error instead of the
// two events.
func TestScanEventStreamStopsAtTheLimit(t *testing.T) {
	var buf bytes.Buffer
	for i := 0; i < 100000; i++ {
		buf.WriteString(`{"id":"x","origin":"o","seq":1}` + "\n")
	}
	const bound = 4096
	r := &boundedReader{data: buf.Bytes(), limit: bound}

	visited := 0
	err := scanEventStream(r, func(*Event) bool {
		visited++
		return visited < 2
	})
	if err != nil {
		t.Fatalf("the scan ran past the %d bytes its caller needed: %v", bound, err)
	}
	if visited != 2 {
		t.Fatalf("visited %d events, want 2", visited)
	}
	if r.pos > 64*1024 {
		t.Errorf("read %d bytes to visit 2 events; the read is not bounded by the caller's need", r.pos)
	}
}

// TestTheEventLogHasNoWholeFileReader: reading the log whole is what made a sync exchange cost
// as much memory as the log is long. The deleted reader is named here so it cannot come back
// quietly, and pendingEvents is checked to still stream.
func TestTheEventLogHasNoWholeFileReader(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob *.go: %v", err)
	}
	scanned := 0
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		body, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		scanned++
		if bytes.Contains(body, []byte("readEventLines")) {
			t.Errorf("%s still refers to readEventLines; the log is streamed, never collected", f)
		}
	}
	if scanned < 20 {
		t.Fatalf("scanned only %d source file(s), the guard proves nothing", scanned)
	}

	sync, err := os.ReadFile("sync.go")
	if err != nil {
		t.Fatalf("read sync.go: %v", err)
	}
	body := string(sync)
	start := strings.Index(body, "func pendingEvents(")
	if start < 0 {
		t.Fatal("pendingEvents is gone; this guard is checking nothing")
	}
	end := strings.Index(body[start:], "\n}\n")
	if end < 0 {
		t.Fatal("pendingEvents has no body to inspect")
	}
	if !strings.Contains(body[start:start+end], "scanEvents(") {
		t.Error("pendingEvents no longer streams the log")
	}
}
