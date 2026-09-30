//go:build !esa

package main

import (
	"bytes"
	"context"
	"log"
	"os"
	"os/exec"
	"strings"
	"time"
)

// The command outlet, for a deployment whose edge is not compiled in.
//
//	SILPHUU_PURGE_CMD      One shell command, run through `sh -c`. The URLs arrive on stdin,
//	                       one full URL per line, and exit status 0 means the edge accepted
//	                       them. Anything else is a failure and is reported as one.
//	SILPHUU_PURGE_TIMEOUT  Seconds before the command is killed and the flush counted as failed
//	                       (default 15). A command that hangs would otherwise hold its
//	                       goroutine for as long as it likes.
//
// Unset means this deployment has no edge to tell, and the flush is skipped. That is the same
// shape as any other optional capability here — an unset SYNC_UDS_SOCK skips the whole sync
// channel — not a vendor-specific on/off switch.
//
// URLs go on stdin rather than in argv: the count varies with the site, they carry query
// strings and non-ASCII path segments, and argv would need quoting plus ARG_MAX arithmetic for
// all of it. A line each needs neither.
const (
	envPurgeCmd     = "SILPHUU_PURGE_CMD"
	envPurgeTimeout = "SILPHUU_PURGE_TIMEOUT"

	defaultPurgeTimeout = 15 * time.Second
)

func init() { setPurgeOutlet(&shellOutlet{}) }

type shellOutlet struct{}

// Ready reports whether the deployment named a command.
func (o *shellOutlet) Ready() bool {
	return strings.TrimSpace(os.Getenv(envPurgeCmd)) != ""
}

// Purge runs the command with the URLs on stdin, one per line.
func (o *shellOutlet) Purge(urls []string) (sent bool, err error) {
	command := strings.TrimSpace(os.Getenv(envPurgeCmd))

	ctx, cancel := context.WithTimeout(context.Background(), purgeTimeout())
	defer cancel()

	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	cmd.Stdin = strings.NewReader(strings.Join(urls, "\n") + "\n")
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out

	err = cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		err = context.DeadlineExceeded
	}
	if err != nil {
		// The URL list goes into the failure line too. The edge rejects the request as a
		// whole, so the only way to tell which URL it objected to is to have the list in
		// front of you; without it the failure is a dead end.
		log.Printf("purge: outlet failed: %v: %s (URLs: %v)", err, strings.TrimSpace(out.String()), urls)
		return false, err
	}
	log.Printf("purge: outlet accepted %d URL(s): %v", len(urls), urls)
	return true, nil
}

// purgeTimeout is the deadline for one flush, from SILPHUU_PURGE_TIMEOUT in seconds.
func purgeTimeout() time.Duration {
	raw := strings.TrimSpace(os.Getenv(envPurgeTimeout))
	if raw == "" {
		return defaultPurgeTimeout
	}
	secs, err := time.ParseDuration(raw + "s")
	if err != nil || secs <= 0 {
		log.Printf("purge: %s=%q is not a positive number of seconds; using %s",
			envPurgeTimeout, raw, defaultPurgeTimeout)
		return defaultPurgeTimeout
	}
	return secs
}
