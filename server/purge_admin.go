package main

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"
)

// The manual side of invalidation.
//
// The automatic path (invalidate.go) decides for itself: it sends only the URLs whose bytes
// moved, and it merges the calls one change produces. This is the deliberate override — the
// administrator names URLs and they go, whether or not the bytes moved, because "get this off
// the edge" is a reason the engine cannot read off the disk.

// PurgeTarget is one URL the administrator can ask the edge to drop.
type PurgeTarget struct {
	Path  string // root-relative, as the rest of the invalidation path speaks
	URL   string // what the outlet receives
	Group string // which source it came from: "page" | "root" | "image"
}

// PurgeGroup is one source of URLs, with its label already resolved to user-facing text.
//
// The label is resolved here rather than in the template because the template has no uiText
// function, and Group is a machine name (see purgeGroups) that must not reach the page.
type PurgeGroup struct {
	Key     string
	Label   string
	Targets []PurgeTarget
}

// clockPlaceholder stands in for the clock inside a flush sentence, so purgeLastParts can cut
// the sentence around it. A NUL byte cannot occur in a ui_strings.json value or in an error
// string, so the cut always lands on the placeholder.
const clockPlaceholder = "\x00"

// purgeLastParts renders the last flush's outcome for the card, split around the clock.
//
// The kinds are machine names (invalidate.go), and the page must not print them: a reader of
// "invalidate.no_outlet" learns nothing about whether their pages reached the edge. The sentence
// is built here so the mapping lives next to the states it names.
//
// The clock is left as a placeholder rather than formatted because the server's timezone is not
// the reader's: a "15:04:05" formatted here is the server's wall clock, and it reads as a wrong
// time to anyone in another zone. The browser draws it from the epoch in at. A sentence with no
// clock placeholder — a state, not a moment — comes back with an empty suffix and a zero at.
func purgeLastParts() (prefix, suffix string, at time.Time) {
	s := lastInvalidationState()
	if s.Kind == "" {
		return uiText("admin.purge.never"), "", time.Time{}
	}
	var sentence string
	switch s.Kind {
	case "invalidate.ok":
		sentence = fmt.Sprintf(uiText("admin.purge.last_ok"), s.Count, clockPlaceholder)
	case "invalidate.failed":
		sentence = fmt.Sprintf(uiText("admin.purge.last_failed"), clockPlaceholder, s.Err)
	case "invalidate.no_outlet":
		sentence = uiText("admin.purge.last_no_outlet")
	case "invalidate.paused":
		sentence = uiText("admin.purge.last_paused")
	default:
		sentence = fmt.Sprintf(uiText("admin.purge.last_skipped"), clockPlaceholder)
	}
	prefix, suffix, found := strings.Cut(sentence, clockPlaceholder)
	if !found {
		// The sentence names a state, not a moment: the card would otherwise glue the last
		// flush's clock onto it and read as if the state had begun then.
		return prefix, "", time.Time{}
	}
	return prefix, suffix, s.At
}

// handleAdminPurgeStatus reports the card's status row, read at the moment of the request.
//
// It is an endpoint rather than template fields because all of it is PROCESS STATE, and the page
// that shows it is served from disk and is itself in the purge set. Rendered there, its bytes would
// change on every flush, so the page could never agree with the ledger the flush compares against —
// one wasted URL per full invalidation, forever.
//
// The sentence is localized here, not in the browser: the mapping from a flush kind to a sentence
// lives in purgeLastParts, and a second copy of it in JavaScript would drift. Only the clock is
// the browser's — it arrives as an epoch and is drawn in the reader's own timezone.
func handleAdminPurgeStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	// Read now, not from a cache: a cached copy of this is a copy of another moment, which is
	// the same mistake as rendering it into the page.
	w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate")

	configured := purgeOutletConfigured()
	outletText := uiText("admin.purge.outlet_no")
	if configured {
		outletText = uiText("admin.purge.outlet_yes")
	}
	paused := purgePaused()
	switchText := uiText("admin.purge.switch_on")
	if paused {
		switchText = uiText("admin.purge.switch_off")
	}
	prefix, suffix, at := purgeLastParts()
	// A zero time has no epoch to send: UnixMilli of the zero value is a large negative number,
	// which the browser would draw as a real date. The card shows no clock then.
	var atMS int64
	if !at.IsZero() {
		atMS = at.UnixMilli()
	}
	writeJSONResponse(w, map[string]interface{}{
		"outlet_ok":   configured,
		"outlet":      outletText,
		"paused":      paused,
		"switch_text": switchText,
		"ledger":      purgeLedgerCount(),
		"last_prefix": prefix,
		"last_suffix": suffix,
		"last_at_ms":  atMS,
	})
}

// purgeGroups lists what a manual purge can name, grouped by where it comes from: every page the
// render cache holds, the files that live at the served root, and the site's own images. Read
// off the disk rather than listed here, so a new page or image needs no change to this.
//
// A group with no targets is not returned: an empty heading offers nothing to select.
func purgeGroups() []PurgeGroup {
	seen := map[string]bool{}
	byKey := map[string]*PurgeGroup{}
	add := func(u, group string) {
		if u == "" || seen[u] {
			return
		}
		seen[u] = true
		g := byKey[group]
		if g == nil {
			g = &PurgeGroup{Key: group, Label: uiText("admin.purge.group." + group)}
			byKey[group] = g
		}
		g.Targets = append(g.Targets, PurgeTarget{Path: urlToPath(u), URL: u, Group: group})
	}

	for _, p := range cachedPageURLs() {
		add(cdnURL(p), "page")
	}
	for _, p := range purgeAlwaysPaths() {
		add(cdnURL(p), "root")
	}
	for _, u := range staticImageURLs() {
		add(u, "image")
	}

	out := make([]PurgeGroup, 0, len(byKey))
	for _, key := range []string{"page", "root", "image"} {
		g := byKey[key]
		if g == nil {
			continue
		}
		sort.Slice(g.Targets, func(i, j int) bool { return g.Targets[i].Path < g.Targets[j].Path })
		out = append(out, *g)
	}
	return out
}

// urlToPath is the inverse of cdnURL: what a URL of this site names, root-relative.
func urlToPath(u string) string {
	p := strings.TrimPrefix(u, cdnBaseURL())
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return p
}

// normalizePurgeTarget accepts what an administrator is likely to paste — a root-relative path,
// or a full URL of this site — and returns the path the invalidation path speaks. A blank line
// and a "#" comment are skipped, so a pasted list can carry notes.
func normalizePurgeTarget(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" || strings.HasPrefix(s, "#") {
		return ""
	}
	if strings.HasPrefix(s, cdnBaseURL()) {
		s = strings.TrimPrefix(s, cdnBaseURL())
	}
	if !strings.HasPrefix(s, "/") {
		s = "/" + s
	}
	return s
}

// purgeOutletConfigured reports whether this node has an outlet it can use. The card shows it
// because an unconfigured node answers every purge with "skipped", and an administrator who
// does not know that reads the silence as success.
func purgeOutletConfigured() bool {
	o := getPurgeOutlet()
	return o != nil && o.Ready()
}

// purgeLedgerCount is how many URLs the edge is known to have been told about.
func purgeLedgerCount() int {
	return len(loadPurgeLedger())
}

// recordPurged writes the current fingerprint of these URLs into the ledger, so the automatic
// path does not send them again for a change it has already covered. It has to record the same
// value the automatic path compares against, which is every variant the edge can receive.
func recordPurged(urls []string) {
	if len(urls) == 0 {
		return
	}
	ledger := loadPurgeLedger()
	for _, u := range urls {
		if sum, ok := servedDigestForURL(u); ok {
			ledger[u] = sum
		}
	}
	prunePurgeLedger(ledger)
	savePurgeLedger(ledger)
}

// handleCommitPurge purges what the administrator named, now.
//
// It calls the outlet directly rather than queueing: the queue waits for renders to finish and
// then drops whatever did not change, and a manual purge is precisely the case where the
// administrator has decided the edge's copy has to go regardless.
func handleCommitPurge(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()

	var paths []string
	switch scope := r.FormValue("scope"); scope {
	case "all":
		paths = fullInvalidationPaths()
	case "custom":
		for _, line := range strings.Split(r.FormValue("urls"), "\n") {
			if p := normalizePurgeTarget(line); p != "" {
				paths = append(paths, p)
			}
		}
	default: // "selected", the checkboxes
		paths = append(paths, r.Form["url"]...)
	}

	urls := make([]string, 0, len(paths))
	for _, p := range paths {
		if u := cdnURL(normalizePurgeTarget(p)); u != "" {
			urls = append(urls, u)
		}
	}
	urls = dedupeNonEmpty(urls)
	if len(urls) == 0 {
		adminRespond(w, r, uiText("admin.purge.nothing"))
		return
	}

	if !purgeOutletConfigured() {
		adminRespond(w, r, uiText("admin.purge.no_outlet"))
		return
	}
	// Checked before the outlet is called, so a paused node answers with the brake rather than
	// falling into the "no outlet" reply below — which would contradict the switch that is on.
	if purgePaused() {
		adminRespond(w, r, uiText("admin.purge.paused_block"))
		return
	}
	sent, err := runPurgeOutlet(urls)
	if err != nil {
		adminRespond(w, r, uiText("admin.purge.failed")+": "+err.Error())
		return
	}
	if !sent {
		adminRespond(w, r, uiText("admin.purge.no_outlet"))
		return
	}
	recordPurged(urls)
	adminRespond(w, r, fmt.Sprintf(uiText("admin.purge.done"), len(urls)))
}

// handleCommitPurgeSwitch applies the pause marker.
//
// Resuming re-queues the full invalidation set rather than trusting the queue: the worker empties
// the queue as it takes it (takeDirtyURLs), so the changes that arrived during the pause are
// already gone from it. Re-queueing costs no quota — the worker compares fingerprints and sends
// only the URLs whose bytes actually moved.
func handleCommitPurgeSwitch(w http.ResponseWriter, r *http.Request, paused bool) {
	if err := setPurgePaused(paused); err != nil {
		adminRespond(w, r, uiText("admin.purge.failed")+": "+err.Error())
		return
	}
	if paused {
		adminRespond(w, r, uiText("admin.purge.paused"))
		return
	}
	invalidatePaths(fullInvalidationPaths())
	adminRespond(w, r, uiText("admin.purge.resumed"))
}
