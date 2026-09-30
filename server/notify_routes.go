package main

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
)

// Notification routing: which channels each kind of message goes out on.
//
// It is a server's own setting, so it lives in the settings directory (SILPHUU_SETTINGS_DIR)
// rather than in config/ or state/: config/ is the site's content and state/ is what the engine
// measures for itself, and this is neither.
//
// The file is written straight to disk and deliberately does NOT travel through the event log.
// Two nodes carry different channel sets — a mail channel configured on one is not on the other
// — so a routing table that synced would name channels the peer does not have and silence that
// event there. Silence is the one failure this setting exists to make visible.

// loadNotifyRoutes reads the routing table. A missing file, or one that does not parse, is no
// routing at all: every event then falls back to the audience default, which is the behaviour
// of a deployment that has never opened the settings panel.
func loadNotifyRoutes() map[string][]string {
	path := FileNotifyRoutes
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var routes map[string][]string
	if err := json.Unmarshal(raw, &routes); err != nil {
		log.Printf("notify: %s does not parse as a routing table (%v); every event falls back to the audience default", path, err)
		return nil
	}
	return routes
}

// saveNotifyRoutes writes the routing table, atomically and keeping keys this build does not
// know: a table hand-edited for a future event must survive a save from the panel.
//
// Atomic because a half-written table reads back as no routing at all, which would silently
// change what every event does. config/'s writers take a plain os.WriteFile; this one cannot.
func saveNotifyRoutes(routes map[string][]string) error {
	path := FileNotifyRoutes
	if err := os.MkdirAll(RootSettings, 0o755); err != nil {
		return err
	}

	merged := map[string][]string{}
	if raw, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(raw, &merged)
	}
	for name, channels := range routes {
		// An empty route is how the panel switches an event off, and it has to marshal as
		// [] rather than null: the key's presence is what says "no fallback".
		if channels == nil {
			channels = []string{}
		}
		merged[name] = channels
	}

	data, err := json.MarshalIndent(merged, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')

	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// handleAdminNotifySave records which channels each kind of message goes out on. It runs no
// permission check of its own — the gateway fronting /admin/ decides who reaches it.
//
// Every event is written explicitly, including the ones left at their default. A half-filled
// table would make the environment variables and the file disagree about which of them decides,
// and that is the kind of ambiguity this setting exists to remove.
func handleAdminNotifySave(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		adminError(w, r, "bad form", http.StatusBadRequest)
		return
	}

	set := resolveNotifiers()
	routes := make(map[string][]string, len(notifyEvents))
	for _, e := range notifyEvents {
		var names []string
		for _, raw := range r.Form["route."+e.Event] {
			name := strings.TrimSpace(raw)
			if name == "" {
				continue
			}
			channel, ok := set.byName[name]
			if !ok {
				adminError(w, r, "save error: "+e.Event+" names "+name+", which this node has no channel for", http.StatusBadRequest)
				return
			}
			if e.Visitor && channel.Kind() != KindMail {
				adminError(w, r, "save error: "+e.Event+" reaches a visitor, so it takes mail channels only", http.StatusBadRequest)
				return
			}
			names = append(names, name)
		}
		sortChannels(names)
		if names == nil {
			names = []string{}
		}
		routes[e.Event] = names
	}

	if err := saveNotifyRoutes(routes); err != nil {
		adminError(w, r, "save error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	// The panel is rendered into /dashboard, and nothing else draws it: this page is the only
	// thing the save moved.
	invalidateCache(PageDashboard)
	// Answer with the new rendering: the invalidation above only queued the page for the edge,
	// and the render behind it is the watcher's. Both spellings of the response send the
	// administrator to the page, so this cannot sit behind either branch.
	warmPageNow(PageDashboard)
	adminRespond(w, r, "")
}

// sampleMessageFor builds the message an event carries, from placeholder data. It is what the
// panel's test button sends.
//
// The placeholders go through the same builders the real path uses, so a channel that mangles a
// test mangles the real notification the same way — which is the whole point of the button. The
// visitor event has no sample: its recipient is an address a visitor typed in, which no test can
// supply.
func sampleMessageFor(event string) (Message, bool) {
	switch event {
	case EventCommentNew:
		return buildCommentNewMessage("测试访客", "这是一条测试评论。", "测试页面", siteBaseURL()+"/post/1"), true
	case EventFriendApply:
		return buildFriendApplyMessage(friendApplyEmailData{
			Name:        "测试站点",
			URL:         "https://example.com",
			Desc:        "这是一个测试友链。",
			Tag:         "测试",
			Email:       "test@example.com",
			TempSVGPath: "staging/friend/example.svg",
			PageURL:     siteBaseURL() + PageFriends,
		}), true
	case EventStickerApply:
		return buildStickerApplyMessage(stickerEmailData{
			Group:   "测试表情包",
			Email:   "test@example.com",
			Count:   3,
			Staged:  "staging/sticker/example",
			PageURL: siteBaseURL() + PageDashboard,
		}), true
	case EventInvalidateFailed:
		return buildInvalidateFailedMessage(errors.New("test failure"), []string{siteBaseURL() + "/"}), true
	case EventBlobFailed:
		return buildBlobFailedMessage(errors.New("test failure"), []string{"static/image/post/example.avif"}), true
	}
	return Message{}, false
}

// handleAdminNotifyTest sends one event's sample message to the channels the panel has ticked for
// it. It runs no permission check of its own — the gateway fronting /admin/ decides who reaches
// it.
//
// Nothing here writes: no route is recorded, no event is committed, no page is re-rendered. A
// test is a delivery and nothing else.
func handleAdminNotifyTest(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		adminError(w, r, "bad form", http.StatusBadRequest)
		return
	}

	event := strings.TrimSpace(r.FormValue("event"))
	evt, known := notifyEventNamed(event)
	if !known {
		adminError(w, r, "test error: "+event+" is not an event this build sends", http.StatusBadRequest)
		return
	}
	if evt.Visitor {
		adminError(w, r, "test error: "+event+" reaches a visitor, so it has no administrator channel to test", http.StatusBadRequest)
		return
	}

	msg, ok := sampleMessageFor(event)
	if !ok {
		adminError(w, r, "test error: "+event+" has no sample message", http.StatusBadRequest)
		return
	}

	// The same admission rule the save applies: a channel this node holds no credentials for
	// is not a channel, whether it was named by a form or by a hand-written request.
	set := resolveNotifiers()
	var targets []Notifier
	for _, raw := range r.Form["channel"] {
		name := strings.TrimSpace(raw)
		if name == "" {
			continue
		}
		channel, found := set.byName[name]
		if !found {
			adminError(w, r, "test error: "+name+" is not a channel this node has", http.StatusBadRequest)
			return
		}
		targets = append(targets, channel)
	}
	if len(targets) == 0 {
		adminError(w, r, "test error: no channel selected", http.StatusBadRequest)
		return
	}

	// One deadline for the whole test, and the answer waits for every channel: returning first
	// would cancel r.Context() on deliveries still in flight. The failures are logged rather
	// than shown — the page reports nothing — but a channel that cannot deliver at all (a mail
	// channel with no NOTIFY_MAIL_TO, say) is exactly what the test exists to surface.
	ctx, cancel := context.WithTimeout(r.Context(), notifyHTTPTimeout)
	defer cancel()
	var wg sync.WaitGroup
	for _, n := range targets {
		wg.Add(1)
		go func(n Notifier) {
			defer wg.Done()
			deliverCtx(ctx, n, msg)
		}(n)
	}
	wg.Wait()

	adminRespond(w, r, "")
}
