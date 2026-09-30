package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// notifyTestEnv points the settings directory at a temp dir and returns the routing file's
// path. The routing file is only read when a deployment named a directory, so every test that
// exercises routing has to go through here.
func notifyTestEnv(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	settings := t.TempDir()
	t.Setenv(envSettingsDir, settings)
	InitRoots(home, filepath.Join(home, "served"))
	t.Cleanup(func() {
		// Unset before re-resolving: InitRoots reads the environment, so leaving the variable
		// in place would hand the next test this one's settings directory.
		os.Unsetenv(envSettingsDir)
		InitRoots("")
	})
	return filepath.Join(settings, "notify.json")
}

// writeRoutes records a routing table the way the panel does.
func writeRoutes(t *testing.T, file string, routes map[string][]string) {
	t.Helper()
	if err := saveNotifyRoutes(routes); err != nil {
		t.Fatalf("saveNotifyRoutes: %v", err)
	}
	if _, err := os.Stat(file); err != nil {
		t.Fatalf("the routing file was not written: %v", err)
	}
}

// buildEveryChannelConfig makes every channel this build knows available, so a test can name any
// of them in a route.
func buildEveryChannelConfig(t *testing.T) {
	t.Helper()
	t.Setenv(envNotifyChannels, "resend,smtp,telegram,gotify,bark,webhook")
	t.Setenv("NOTIFY_RESEND_API_KEY", "key")
	t.Setenv(envSMTPAddr, "smtp.example.com:465")
	t.Setenv(envSMTPUser, "noreply@example.com")
	t.Setenv(envSMTPPass, "secret")
	t.Setenv("NOTIFY_TELEGRAM_BOT_TOKEN", "token")
	t.Setenv("NOTIFY_TELEGRAM_CHAT_ID", "chat")
	t.Setenv("NOTIFY_GOTIFY_URL", "https://gotify.example.com")
	t.Setenv("NOTIFY_GOTIFY_TOKEN", "token")
	t.Setenv("NOTIFY_BARK_KEY", "devicekey")
	t.Setenv("NOTIFY_WEBHOOK_URL", "https://hook.example.com")
	t.Setenv("NOTIFY_MAIL_FROM", "noreply@example.com")
}

// A recorded route is what one event goes out on, and an event with none is still reached the
// way the audience variables say. Both halves matter: the second is what lets a deployment that
// never opened the panel keep working, and the first is the whole point of the setting.
func TestARouteReplacesTheAudienceDefaultForItsEventOnly(t *testing.T) {
	notifyTestEnv(t)
	buildEveryChannelConfig(t)
	t.Setenv(envNotifyAdminVia, "telegram")

	writeRoutes(t, FileNotifyRoutes, map[string][]string{EventCommentNew: {"bark", "gotify"}})

	set := resolveNotifiers()
	if got := notifierNames(set.channelsFor(EventCommentNew, set.admin)); !reflect.DeepEqual(got, []string{"bark", "gotify"}) {
		t.Errorf("comment.new routes to %v, want the recorded [bark gotify]", got)
	}
	if got := notifierNames(set.channelsFor(EventFriendApply, set.admin)); !reflect.DeepEqual(got, []string{"telegram"}) {
		t.Errorf("friend.apply routes to %v, want the audience default [telegram]", got)
	}
}

// An empty route is how an event is switched off. It has to be distinguishable from no route at
// all, which is why the key's presence and not the list's length is what the routing reads.
func TestAnEmptyRouteSwitchesTheEventOff(t *testing.T) {
	notifyTestEnv(t)
	buildEveryChannelConfig(t)

	writeRoutes(t, FileNotifyRoutes, map[string][]string{EventStickerApply: {}})

	set := resolveNotifiers()
	if got := set.channelsFor(EventStickerApply, set.admin); len(got) != 0 {
		t.Errorf("sticker.apply routes to %v, want nothing at all", notifierNames(got))
	}
	if got := set.channelsFor(EventCommentNew, set.admin); len(got) == 0 {
		t.Errorf("comment.new has no route and no audience default, so nothing could ever be sent")
	}
}

// A route can name a channel this node holds no credentials for — a hand-edited table, or a
// table written before a credential was removed. It is dropped rather than fatal, and the
// remaining names still go out.
func TestARouteToAChannelThisNodeLacksIsDropped(t *testing.T) {
	notifyTestEnv(t)
	t.Setenv(envNotifyChannels, "telegram")
	t.Setenv("NOTIFY_TELEGRAM_BOT_TOKEN", "token")
	t.Setenv("NOTIFY_TELEGRAM_CHAT_ID", "chat")

	writeRoutes(t, FileNotifyRoutes, map[string][]string{EventCommentNew: {"bark", "telegram"}})

	set := resolveNotifiers()
	if got := notifierNames(set.channelsFor(EventCommentNew, set.admin)); !reflect.DeepEqual(got, []string{"telegram"}) {
		t.Errorf("comment.new routes to %v, want the one channel this node has", got)
	}
}

// The mail-only rule is applied to a routed result, not only to the audience default. A table
// that names a chat channel for the reply event must not be able to put a visitor's message into
// the operator's own chat.
func TestAVisitorRouteCannotReachAChatChannel(t *testing.T) {
	chat := &recordingChannel{name: "chat"}
	mail := &recordingChannel{name: "mail", kind: KindMail}
	saved := notifiers
	notifiers = &notifierSet{
		byName:  map[string]Notifier{"chat": chat, "mail": mail},
		routes:  map[string][]string{EventCommentReply: {"chat", "mail"}},
		visitor: []Notifier{mail},
	}
	t.Cleanup(func() { notifiers = saved })

	notifyVisitor(Message{Event: EventCommentReply, To: "visitor@example.com", Subject: "s"})

	if len(chat.got) != 0 {
		t.Errorf("a visitor's message reached the operator's chat: %v", chat.got)
	}
	if len(mail.got) != 1 {
		t.Errorf("the mail channel received %d messages, want 1", len(mail.got))
	}
}

// recordingChannel records what it was asked to deliver, so a test can ask which channels a
// message reached without standing up an HTTP endpoint.
type recordingChannel struct {
	name string
	kind string
	got  []Message
}

func (c *recordingChannel) Name() string     { return c.name }
func (c *recordingChannel) Kind() string     { return c.kind }
func (c *recordingChannel) Endpoint() string { return c.name }
func (c *recordingChannel) Deliver(_ context.Context, msg Message) error {
	c.got = append(c.got, msg)
	return nil
}

// The panel's save is the only writer of the routing file, and every event it shows is written
// explicitly. A half-filled table would leave the file and the environment variables disagreeing
// about which of them decides.
func TestAdminNotifySaveRecordsEveryEvent(t *testing.T) {
	notifyTestEnv(t)
	buildEveryChannelConfig(t)

	form := url.Values{}
	form.Add("route."+EventCommentNew, "bark")
	form.Add("route."+EventCommentNew, "gotify")
	form.Add("route."+EventStickerApply, "webhook")
	post := httptest.NewRequest("POST", "/admin/commit/notify", strings.NewReader(form.Encode()))
	post.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	handleAdminNotifySave(rec, post)

	// The panel's form is a plain one, so the answer is the redirect that lands the browser back
	// on /dashboard — which is what makes the panel and the checkboxes show the new table.
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("save answered %d: %s", rec.Code, rec.Body.String())
	}
	routes := loadNotifyRoutes()
	if got := routes[EventCommentNew]; !reflect.DeepEqual(got, []string{"bark", "gotify"}) {
		t.Errorf("comment.new = %v, want [bark gotify]", got)
	}
	if got, ok := routes[EventStickerApply]; !ok || !reflect.DeepEqual(got, []string{"webhook"}) {
		t.Errorf("sticker.apply = %v (present %v), want [webhook]", got, ok)
	}
	// An event the form said nothing about is recorded as switched off, not left out: the panel
	// posts every group, so a missing one means the caller did not send the whole form.
	if got, ok := routes[EventBlobFailed]; !ok || len(got) != 0 {
		t.Errorf("blob.failed = %v (present %v), want a recorded empty route", got, ok)
	}
	if n := len(routes); n != len(notifyEvents) {
		t.Errorf("the table holds %d events, want one entry per event (%d)", n, len(notifyEvents))
	}
}

// A route naming a channel this node cannot build is refused rather than recorded: the file is
// what the routing reads, so recording it would leave a table that silently sends nothing.
func TestAdminNotifySaveRefusesAChannelThisNodeLacks(t *testing.T) {
	notifyTestEnv(t)
	t.Setenv(envNotifyChannels, "telegram")
	t.Setenv("NOTIFY_TELEGRAM_BOT_TOKEN", "token")
	t.Setenv("NOTIFY_TELEGRAM_CHAT_ID", "chat")

	form := url.Values{}
	form.Add("route."+EventCommentNew, "bark")
	post := httptest.NewRequest("POST", "/admin/commit/notify", strings.NewReader(form.Encode()))
	post.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	handleAdminNotifySave(rec, post)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("save answered %d, want 400 for a channel this node has no credentials for", rec.Code)
	}
	if _, err := os.Stat(FileNotifyRoutes); err == nil {
		t.Errorf("the refused save still wrote %s", FileNotifyRoutes)
	}
}

// The reply event reaches an address the visitor typed, so the save refuses a chat channel for
// it. The routing drops such a name too; refusing it here is what keeps a table that says one
// thing and does another out of the file in the first place.
func TestAdminNotifySaveRefusesANonMailChannelForTheVisitorEvent(t *testing.T) {
	notifyTestEnv(t)
	buildEveryChannelConfig(t)

	form := url.Values{}
	form.Add("route."+EventCommentReply, "bark")
	post := httptest.NewRequest("POST", "/admin/commit/notify", strings.NewReader(form.Encode()))
	post.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	handleAdminNotifySave(rec, post)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("save answered %d, want 400 for a chat channel on the visitor event", rec.Code)
	}
}

// A deployment that names no settings directory still has one: the conventional location, so the
// routing file resolves there rather than the setting being unusable out of the box.
func TestAnUnnamedSettingsDirectoryLandsOnTheConventionalLocation(t *testing.T) {
	t.Setenv(envSettingsDir, "")
	InitRoots(t.TempDir())
	t.Cleanup(func() { InitRoots("") })

	if RootSettings != defaultSettingsDir {
		t.Errorf("settings root = %q, want %q", RootSettings, defaultSettingsDir)
	}
	if want := filepath.Join(defaultSettingsDir, "notify.json"); FileNotifyRoutes != want {
		t.Errorf("the routing file = %q, want %q", FileNotifyRoutes, want)
	}
}

// A settings file written into the engine tree would be published with the sources, so the
// directory is refused there. The engine tree is what the release is filtered from.
func TestASettingsDirectoryInsideTheEngineTreeIsRefused(t *testing.T) {
	t.Setenv(envSettingsDir, filepath.Join(engineRoot, "state-settings"))
	InitRoots(t.TempDir())
	t.Cleanup(func() {
		os.Unsetenv(envSettingsDir)
		InitRoots("")
	})

	if err := checkSettingsOutsideEngine(); err == nil {
		t.Errorf("a settings directory under %s was accepted", engineRoot)
	}

	// And the same check passes for a directory outside it, so the guard is not simply always on.
	t.Setenv(envSettingsDir, t.TempDir())
	InitRoots(t.TempDir())
	if err := checkSettingsOutsideEngine(); err != nil {
		t.Errorf("a settings directory outside the engine tree was refused: %v", err)
	}
}

// A routing file hand-edited for an event this build does not know must survive a save from the
// panel: the panel only writes the events it shows, and dropping the rest would silently delete
// a setting a newer build depends on.
func TestASaveKeepsRoutesThisBuildDoesNotKnow(t *testing.T) {
	file := notifyTestEnv(t)

	raw := []byte("{\n  \"future.event\": [\"telegram\"]\n}\n")
	if err := os.WriteFile(file, raw, 0o644); err != nil {
		t.Fatalf("seed the file: %v", err)
	}

	if err := saveNotifyRoutes(map[string][]string{EventCommentNew: {"smtp"}}); err != nil {
		t.Fatalf("saveNotifyRoutes: %v", err)
	}

	var merged map[string][]string
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if err := json.Unmarshal(data, &merged); err != nil {
		t.Fatalf("the saved file does not parse: %v", err)
	}
	if got := merged["future.event"]; !reflect.DeepEqual(got, []string{"telegram"}) {
		t.Errorf("future.event = %v, want it kept", got)
	}
}

// The save answers with the page it moved, so the administrator has to be shown the new table by
// then. The invalidation only queues the page for the edge, and the render behind it is the
// watcher's: a save that stops there leaves the previous table on the page, and the edge's
// refetch reads it back and caches it again — the purge buys nothing.
func TestSavingRoutesRewritesTheDashboardBeforeAnswering(t *testing.T) {
	// Set before useExampleSite: that is what resolves the roots, and the settings directory is
	// read from the environment at that moment.
	t.Setenv(envSettingsDir, t.TempDir())
	useExampleSite(t)
	wireTestMux(t)
	initTemplates()
	backfillPosts()
	t.Setenv("DEV_MODE", "")
	t.Setenv(envNotifyChannels, "telegram,smtp")
	t.Setenv("NOTIFY_TELEGRAM_BOT_TOKEN", "tok")
	t.Setenv("NOTIFY_TELEGRAM_CHAT_ID", "chat")
	t.Setenv(envSMTPAddr, "smtp.example.com:465")
	t.Setenv(envSMTPUser, "noreply@example.com")
	t.Setenv(envSMTPPass, "secret")
	t.Setenv("NOTIFY_MAIL_FROM", "noreply@example.com")

	// The panel has to be on the page before the save, or "it changed" would hold for a page
	// that never carried it.
	warmPageNow(PageDashboard)
	before, err := os.ReadFile(cachePath(PageDashboard))
	if err != nil {
		t.Fatalf("the dashboard was not rendered: %v", err)
	}
	// No route is recorded, so the administrator's own default reaches both channels.
	if !panelBoxTicked(before, EventCommentNew, "smtp") {
		t.Fatalf("%s does not reach smtp before the save, so the change below is untestable", EventCommentNew)
	}

	form := url.Values{}
	form.Add("route."+EventCommentNew, "telegram")
	post := httptest.NewRequest("POST", "/admin/commit/notify", strings.NewReader(form.Encode()))
	post.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	handleAdminNotifySave(httptest.NewRecorder(), post)

	after, err := os.ReadFile(cachePath(PageDashboard))
	if err != nil {
		t.Fatalf("the dashboard was not re-rendered: %v", err)
	}
	if !panelBoxTicked(after, EventCommentNew, "telegram") {
		t.Errorf("%s does not reach telegram on the page the save answered with", EventCommentNew)
	}
	if panelBoxTicked(after, EventCommentNew, "smtp") {
		t.Errorf("%s still reaches smtp on the page the save answered with, so the edge's refetch "+
			"reads the previous table back", EventCommentNew)
	}
}

// panelBoxTicked reports whether the panel's box for one event's channel is ticked. The rendered
// page is minified, so the attributes are compared with their quotes stripped.
func panelBoxTicked(page []byte, event, channel string) bool {
	flat := bytes.ReplaceAll(page, []byte(`"`), nil)
	return bytes.Contains(flat, []byte("name=route."+event+" value="+channel+" checked"))
}

// The routing table is written by the panel's handler and by nothing else. In particular no
// event projection may touch it: an event reaches the peer, and a route that reached the peer
// would name channels that node may not have, silencing the event there. This is the one
// config-shaped file whose write path is deliberately outside the log, so the rule needs a guard
// rather than a comment.
func TestNotifyRoutesHaveOneWriterAndItIsNotTheLog(t *testing.T) {
	const allowed = "notify_routes.go"

	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob *.go: %v", err)
	}
	scanned, hits := 0, 0
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		body, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		scanned++
		if !bytes.Contains(body, []byte("saveNotifyRoutes(")) {
			continue
		}
		hits++
		if f != allowed {
			t.Errorf("%s writes the routing table; the panel's handler in %s owns it", f, allowed)
		}
	}

	if scanned < 20 {
		t.Fatalf("scanned only %d source file(s)", scanned)
	}
	if hits != 1 {
		t.Errorf("found the routing writer in %d file(s), want exactly %s", hits, allowed)
	}
}

// The settings panel offers a channel only when this node can build it, because a route to one
// it cannot is refused. The visitor event offers mail channels only, matching the routing rule.
func TestRouteGroupsOfferOnlyWhatTheSaveAccepts(t *testing.T) {
	notifyTestEnv(t)
	t.Setenv(envNotifyChannels, "telegram,smtp")
	t.Setenv("NOTIFY_TELEGRAM_BOT_TOKEN", "token")
	t.Setenv("NOTIFY_TELEGRAM_CHAT_ID", "chat")
	t.Setenv(envSMTPAddr, "smtp.example.com:465")
	t.Setenv(envSMTPUser, "noreply@example.com")
	t.Setenv(envSMTPPass, "secret")
	t.Setenv("NOTIFY_MAIL_FROM", "noreply@example.com")

	groups := notifyRouteGroups()
	if len(groups) != len(notifyEvents) {
		t.Fatalf("the panel shows %d groups, want one per event (%d)", len(groups), len(notifyEvents))
	}

	for _, g := range groups {
		if g.Label == "" || g.Label == "admin.notify.event."+strings.ReplaceAll(g.Event, ".", "_") {
			t.Errorf("%s has no copy in the library: the panel would print the key path", g.Event)
		}
		for _, o := range g.Options {
			if o.Set && o.Name != "telegram" && o.Name != "smtp" {
				t.Errorf("%s offers %q, which this node cannot build", g.Event, o.Name)
			}
			if g.Visitor && o.Name == "telegram" {
				t.Errorf("the visitor event offers the chat channel %q, which the save refuses", o.Name)
			}
		}
	}

	// A channel this node cannot build is still shown, so "why is bark not offered" has an
	// answer on the page. It is the ones it can build that must be ticked.
	var sawAbsent bool
	for _, g := range groups {
		if g.Visitor {
			continue
		}
		for _, o := range g.Options {
			if !o.Set {
				sawAbsent = true
			}
		}
	}
	if !sawAbsent {
		t.Errorf("no unconfigured channel is listed, so the panel cannot explain why it is missing")
	}
}

// postNotifyTest drives the gateway the way the panel's test button does. The path values are set
// by hand because httptest.NewRequest runs no mux — this is exactly what
// POST /admin/commit/{category}/{action} fills in for the real request.
func postNotifyTest(t *testing.T, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("POST", "/admin/commit/notify/test", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-Requested-With", "fetch")
	req.SetPathValue("category", "notify")
	req.SetPathValue("action", "test")
	rec := httptest.NewRecorder()
	handleCommitGateway(rec, req)
	return rec
}

// countingEndpoint stands up an HTTP endpoint and counts what reaches it.
func countingEndpoint(t *testing.T) (*httptest.Server, *int) {
	t.Helper()
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits++ }))
	t.Cleanup(srv.Close)
	return srv, &hits
}

// The test button sends to what is ticked in its own group and to nothing else. Sending to every
// channel the node has would make the button useless for the question it answers: whether the
// channel the operator intends to use actually works.
func TestNotifyTestSendsOnlyToTheTickedChannels(t *testing.T) {
	notifyTestEnv(t)
	t.Setenv("BASE_URL", "https://probe.example")

	webhook, webhookHits := countingEndpoint(t)
	gotify, gotifyHits := countingEndpoint(t)
	t.Setenv(envNotifyChannels, "webhook,gotify")
	t.Setenv("NOTIFY_WEBHOOK_URL", webhook.URL)
	t.Setenv("NOTIFY_GOTIFY_URL", gotify.URL)
	t.Setenv("NOTIFY_GOTIFY_TOKEN", "token")

	form := url.Values{}
	form.Set("event", EventCommentNew)
	form.Add("channel", "webhook")
	rec := postNotifyTest(t, form)

	if rec.Code != http.StatusOK {
		t.Fatalf("the test send answered %d: %s", rec.Code, rec.Body.String())
	}
	if *webhookHits != 1 {
		t.Errorf("the ticked channel received %d message(s), want 1", *webhookHits)
	}
	if *gotifyHits != 0 {
		t.Errorf("an unticked channel received %d message(s), want none", *gotifyHits)
	}
}

// A test changes nothing: no route is recorded and no page is re-rendered. Both would be writes
// from a button whose only job is to deliver one message.
func TestNotifyTestWritesNothing(t *testing.T) {
	file := notifyTestEnv(t)
	t.Setenv("BASE_URL", "https://probe.example")

	webhook, hits := countingEndpoint(t)
	t.Setenv(envNotifyChannels, "webhook")
	t.Setenv("NOTIFY_WEBHOOK_URL", webhook.URL)

	form := url.Values{}
	form.Set("event", EventCommentNew)
	form.Add("channel", "webhook")
	if rec := postNotifyTest(t, form); rec.Code != http.StatusOK {
		t.Fatalf("the test send answered %d: %s", rec.Code, rec.Body.String())
	}
	if *hits != 1 {
		t.Fatalf("the ticked channel received %d message(s), so the rest of this test proves nothing", *hits)
	}

	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Errorf("the test send wrote the routing table: %v", err)
	}
	if _, err := os.Stat(cachePath(PageDashboard)); !os.IsNotExist(err) {
		t.Errorf("the test send rendered the dashboard: %v", err)
	}
}

// A request the panel never makes is still refused rather than sent: the visitor event has no
// administrator channel, a name this build does not know is not an event, and a channel this node
// holds no credentials for cannot deliver anything.
func TestNotifyTestRefusesWhatItCannotSend(t *testing.T) {
	notifyTestEnv(t)
	buildEveryChannelConfig(t)

	for _, tc := range []struct {
		name string
		form url.Values
	}{
		{"the visitor event", url.Values{"event": {EventCommentReply}, "channel": {"resend"}}},
		{"an event this build does not send", url.Values{"event": {"nope.new"}, "channel": {"resend"}}},
		{"a channel this node lacks", url.Values{"event": {EventCommentNew}, "channel": {"nosuchthing"}}},
		{"no channel at all", url.Values{"event": {EventCommentNew}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if rec := postNotifyTest(t, tc.form); rec.Code != http.StatusBadRequest {
				t.Errorf("answered %d, want 400: %s", rec.Code, rec.Body.String())
			}
		})
	}
}

// The panel's save posts no action, and the test button posts action=test. Both arrive at the
// same category, so this dispatch is what keeps a test from writing the routing table — and a
// save from being mistaken for a delivery.
func TestTheNotifyCategorySplitsTestFromSave(t *testing.T) {
	file := notifyTestEnv(t)
	buildEveryChannelConfig(t)

	form := url.Values{}
	form.Add("route."+EventCommentNew, "bark")
	req := httptest.NewRequest("POST", "/admin/commit/notify", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetPathValue("category", "notify")
	rec := httptest.NewRecorder()
	handleCommitGateway(rec, req)

	if _, err := os.Stat(file); err != nil {
		t.Fatalf("a save with no action did not write the routing table: %v", err)
	}
	if got := loadNotifyRoutes()[EventCommentNew]; !reflect.DeepEqual(got, []string{"bark"}) {
		t.Errorf("comment.new = %v, want [bark]", got)
	}
}
