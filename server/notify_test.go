package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// A visitor notification goes to an address the visitor typed into a comment form, so only a
// channel that delivers to an address it was handed can carry it. A chat channel named in
// NOTIFY_VISITOR_VIA has to be dropped: sending the visitor's message to the operator's own chat
// instead would be worse than not sending it at all.
func TestVisitorRoutingDropsNonMailChannels(t *testing.T) {
	t.Setenv(envNotifyChannels, "resend,telegram")
	t.Setenv(envNotifyVisitorVia, "resend,telegram")
	t.Setenv("NOTIFY_RESEND_API_KEY", "key")
	t.Setenv("NOTIFY_TELEGRAM_BOT_TOKEN", "token")
	t.Setenv("NOTIFY_TELEGRAM_CHAT_ID", "chat")

	set := loadNotifiers()

	if len(set.visitor) != 1 || set.visitor[0].Name() != "resend" {
		t.Errorf("visitor channels = %v, want just resend", notifierNames(set.visitor))
	}
	if len(set.admin) != 2 {
		t.Errorf("administrator channels = %v, want both", notifierNames(set.admin))
	}
}

// With nothing said about visitors, they go over every enabled channel that can reach them.
func TestVisitorDefaultsToTheEnabledMailChannels(t *testing.T) {
	t.Setenv(envNotifyChannels, "resend,gotify")
	t.Setenv(envNotifyVisitorVia, "")
	t.Setenv("NOTIFY_RESEND_API_KEY", "key")
	t.Setenv("NOTIFY_GOTIFY_URL", "https://gotify.example.com")
	t.Setenv("NOTIFY_GOTIFY_TOKEN", "token")

	set := loadNotifiers()

	if len(set.visitor) != 1 || set.visitor[0].Name() != "resend" {
		t.Errorf("visitor channels = %v, want just resend", notifierNames(set.visitor))
	}
}

// A typo in one channel's name must not stop the node from telling the operator about anything
// else — the failure it would cause is silence, which is the one failure nobody can see.
func TestAnUnknownChannelNameIsSkipped(t *testing.T) {
	t.Setenv(envNotifyChannels, "resend,nosuchthing")
	t.Setenv("NOTIFY_RESEND_API_KEY", "key")

	set := loadNotifiers()

	if len(set.admin) != 1 || set.admin[0].Name() != "resend" {
		t.Errorf("administrator channels = %v, want just resend", notifierNames(set.admin))
	}
}

// A channel with no credentials is not a channel. Naming it must not produce an entry that only
// fails when a message has to go out.
func TestAChannelWithoutCredentialsIsAbsent(t *testing.T) {
	t.Setenv(envNotifyChannels, "resend,gotify")
	t.Setenv("NOTIFY_RESEND_API_KEY", "")
	t.Setenv("NOTIFY_GOTIFY_URL", "")
	t.Setenv("NOTIFY_GOTIFY_TOKEN", "")

	if set := loadNotifiers(); len(set.admin) != 0 {
		t.Errorf("administrator channels = %v, want none", notifierNames(set.admin))
	}
}

// The panel's two rows carry the channels that routing uses first, then the channels this build
// knows that the routing could use, the ones with credentials before the ones without. The tail
// is what tells an operator whether a name they are about to put in NOTIFY_CHANNELS is even
// buildable on this node.
//
// The two rows differ in both directions here: gotify carries the administrator's notices but is
// not on the visitor row at all, because a visitor's address is handed in at call time and
// gotify's destination is fixed in the environment. smtp is on the visitor row while carrying
// nothing, because it is a mail channel this node could reach a visitor with.
//
// The values differ too, and that is the point of them: the visitor's row says which address the
// message comes from (the visitor typed their own), the administrator's says which address it
// goes to.
func TestNotifyRowsPutTheUsedChannelsFirst(t *testing.T) {
	t.Setenv(envNotifyChannels, "resend,gotify")
	t.Setenv(envNotifyAdminVia, "resend,gotify")
	t.Setenv(envNotifyVisitorVia, "")
	t.Setenv("NOTIFY_RESEND_API_KEY", "key")
	t.Setenv("NOTIFY_GOTIFY_URL", "https://gotify.example.com")
	t.Setenv("NOTIFY_GOTIFY_TOKEN", "token")
	t.Setenv("NOTIFY_MAIL_FROM", "noreply@example.com")
	t.Setenv(envMailTo, "admin@example.com")

	admin, visitor := notifyRows()

	wantAdmin := []notifyBadge{
		{Name: "resend", Active: true, Value: "admin@example.com"},
		{Name: "gotify", Active: true, Value: "https://gotify.example.com"},
		{Name: "smtp"},
		{Name: "bark"},
		{Name: "webhook"},
		{Name: "telegram"},
	}
	if !reflect.DeepEqual(admin, wantAdmin) {
		t.Errorf("administrator row = %v, want %v", admin, wantAdmin)
	}

	wantVisitor := []notifyBadge{
		{Name: "resend", Active: true, Value: "noreply@example.com"},
		{Name: "smtp"},
	}
	if !reflect.DeepEqual(visitor, wantVisitor) {
		t.Errorf("visitor row = %v, want %v", visitor, wantVisitor)
	}
}

// A badge with no value is a channel this node cannot use, so every channel that was built has to
// name its endpoint. Without this the rows could render bare names and still pass every other
// assertion here.
func TestABuiltChannelAlwaysNamesItsEndpoint(t *testing.T) {
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

	for _, st := range channelStatuses(resolveNotifiers()) {
		if !st.Set {
			t.Fatalf("%s was not built although its credentials are set", st.Name)
		}
		if st.Endpoint == "" {
			t.Errorf("%s is built but names no endpoint, so its badge would show a bare name", st.Name)
		}
	}
}

// The kinds the panel attributes to channels are what keep a chat channel off the visitor row on
// a node that has no credentials for it — the state where Kind() has nothing to answer from. So
// they have to agree with the channels themselves, and building each one is the only way to ask.
func TestKnownChannelKindsMatchTheChannels(t *testing.T) {
	t.Setenv("NOTIFY_RESEND_API_KEY", "key")
	t.Setenv(envSMTPAddr, "smtp.example.com:465")
	t.Setenv(envSMTPUser, "noreply@example.com")
	t.Setenv(envSMTPPass, "secret")
	t.Setenv("NOTIFY_TELEGRAM_BOT_TOKEN", "token")
	t.Setenv("NOTIFY_TELEGRAM_CHAT_ID", "chat")
	t.Setenv("NOTIFY_GOTIFY_URL", "https://gotify.example.com")
	t.Setenv("NOTIFY_GOTIFY_TOKEN", "token")
	t.Setenv("NOTIFY_WEBHOOK_URL", "https://hook.example.com")

	kindOf := map[string]string{}
	for _, k := range knownChannels {
		kindOf[k.Name] = k.Kind
	}
	for _, n := range []Notifier{
		newResendChannel(),
		newSMTPChannel(),
		newTelegramChannel(),
		newGotifyChannel(),
		newWebhookChannel(),
	} {
		if n == nil {
			t.Fatal("a channel with its credentials set was not built, so its kind cannot be checked")
		}
		if got := kindOf[n.Name()]; got != n.Kind() {
			t.Errorf("%s: the panel says kind %q, the channel reports %q", n.Name(), got, n.Kind())
		}
	}
}

// Every list of channels reads in the order knownChannels fixes: the panel, the routing file and
// the startup log. Sorted by name instead, the same set reads as two different products — "bark,
// smtp" in one place and "smtp, bark" in another — and nothing holds them together.
func TestChannelListsFollowOneOrder(t *testing.T) {
	want := []string{"smtp", "resend", "bark", "gotify", "webhook", "telegram"}

	// Scrambled on the way in, and carrying a name this build does not know.
	got := splitList("telegram, webhook, nope, gotify, bark, resend, smtp")
	wantScrambled := append(append([]string{}, want...), "nope")
	if !reflect.DeepEqual(got, wantScrambled) {
		t.Errorf("splitList = %v, want %v (known channels in panel order, an unknown name last)", got, wantScrambled)
	}

	var listed []string
	for _, st := range channelStatuses(resolveNotifiers()) {
		listed = append(listed, st.Name)
	}
	if !reflect.DeepEqual(listed, want) {
		t.Errorf("the panel lists %v, want %v", listed, want)
	}
}

// The webhook posts the whole message as JSON, so the far side decides what to do with the
// fields rather than parsing them back out of a sentence.
func TestWebhookPostsTheMessageAsJSON(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
	}))
	defer srv.Close()

	t.Setenv("NOTIFY_WEBHOOK_URL", srv.URL)
	ch := newWebhookChannel()
	if ch == nil {
		t.Fatal("a configured webhook URL produced no channel")
	}

	msg := Message{Subject: "subject", Body: "body", Fields: map[string]string{"kind": "invalidate.failed"}}
	if err := ch.Deliver(context.Background(), msg); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if got["subject"] != "subject" || got["body"] != "body" {
		t.Errorf("the far side received %v", got)
	}
	if fields, ok := got["fields"].(map[string]any); !ok || fields["kind"] != "invalidate.failed" {
		t.Errorf("the fields did not survive the post: %v", got["fields"])
	}
}

// Bark files a notification under a group: the domain the sending node serves. It follows the
// site's own configuration rather than a literal, so the two nodes' notices are told apart by the
// domain that sent them. NOTIFY_BARK_GROUP overrides it.
func TestBarkFilesNotificationsUnderTheSendingDomain(t *testing.T) {
	useExampleSite(t)
	t.Setenv("NOTIFY_BARK_KEY", "devicekey")
	t.Setenv("BASE_URL", "https://probe.example")

	post := func(t *testing.T) map[string]any {
		t.Helper()
		var got map[string]any
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewDecoder(r.Body).Decode(&got)
		}))
		defer srv.Close()
		t.Setenv("NOTIFY_BARK_URL", srv.URL)

		ch := newBarkChannel()
		if ch == nil {
			t.Fatal("a configured device key produced no channel")
		}
		if err := ch.Deliver(context.Background(), Message{Subject: "subject", Body: "body"}); err != nil {
			t.Fatalf("Deliver: %v", err)
		}
		if got["device_key"] != "devicekey" || got["title"] != "subject" || got["body"] != "body" {
			t.Errorf("the far side received %v", got)
		}
		return got
	}

	if got, want := post(t)["group"], "probe.example"; got != want {
		t.Errorf("group = %v, want %q", got, want)
	}

	t.Setenv("NOTIFY_BARK_GROUP", "mine")
	if got := post(t)["group"]; got != "mine" {
		t.Errorf("group = %v, want the override", got)
	}
}

// The retention is asked for only when it is a positive number of seconds. Anything else — unset,
// unparseable, zero, negative — leaves the field out, which is how a notification is kept with no
// expiry. The push arrives either way; only the saved record is bounded.
func TestBarkSendsOnlyAUsableRetention(t *testing.T) {
	useExampleSite(t)
	t.Setenv("NOTIFY_BARK_KEY", "devicekey")

	for _, tc := range []struct {
		set  string
		want any
	}{
		{"", nil},
		{"600", float64(600)},
		{"abc", nil},
		{"0", nil},
		{"-5", nil},
	} {
		t.Run("ttl="+tc.set, func(t *testing.T) {
			var got map[string]any
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_ = json.NewDecoder(r.Body).Decode(&got)
			}))
			defer srv.Close()

			t.Setenv("NOTIFY_BARK_URL", srv.URL)
			t.Setenv("NOTIFY_BARK_TTL", tc.set)

			ch := newBarkChannel()
			if ch == nil {
				t.Fatal("a configured device key produced no channel")
			}
			if err := ch.Deliver(context.Background(), Message{Subject: "s"}); err != nil {
				t.Fatalf("Deliver: %v", err)
			}
			if tc.want == nil {
				if sent, ok := got["ttl"]; ok {
					t.Errorf("ttl was sent as %v, want the field left out", sent)
				}
				return
			}
			if got["ttl"] != tc.want {
				t.Errorf("ttl = %v, want %v", got["ttl"], tc.want)
			}
		})
	}
}

// A chat channel shows the concise form when the message carries one, and the mail text with its
// fields when it does not — so a message that was never rewritten still reads.
func TestRenderChatFallsBackToTheMailText(t *testing.T) {
	msg := Message{Body: "body", Fields: map[string]string{"count": "2"}}
	if got, want := msg.renderChat(), "body\ncount: 2"; got != want {
		t.Errorf("renderChat with no Chat = %q, want the mail text %q", got, want)
	}
	msg.Chat = "concise"
	if got := msg.renderChat(); got != "concise" {
		t.Errorf("renderChat = %q, want the concise form", got)
	}
}

// The webhook's text field is a machine contract — the mail text with its fields — and a receiver
// parses it. Adding a concise form must leave it alone: routed through the chat form instead, a
// receiver would silently lose every field.
func TestWebhookKeepsTextAsTheMailText(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
	}))
	defer srv.Close()

	t.Setenv("NOTIFY_WEBHOOK_URL", srv.URL)
	ch := newWebhookChannel()
	if ch == nil {
		t.Fatal("a configured webhook URL produced no channel")
	}

	msg := Message{
		Event: EventCommentNew, Subject: "subject", Body: "body",
		Fields: map[string]string{"count": "2"},
		Chat:   "concise", Link: "https://example.com/post/1",
	}
	if err := ch.Deliver(context.Background(), msg); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if got["text"] != msg.renderText() {
		t.Errorf("text = %v, want the mail text %q", got["text"], msg.renderText())
	}
	if got["chat"] != "concise" || got["link"] != "https://example.com/post/1" {
		t.Errorf("the concise form did not reach the far side: %v", got)
	}
}

// barkPayload delivers one message to a stand-in bark server and returns what it received.
func barkPayload(t *testing.T, msg Message) map[string]any {
	t.Helper()
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
	}))
	defer srv.Close()

	t.Setenv("NOTIFY_BARK_URL", srv.URL)
	t.Setenv("NOTIFY_BARK_KEY", "devicekey")
	ch := newBarkChannel()
	if ch == nil {
		t.Fatal("a configured device key produced no channel")
	}
	if err := ch.Deliver(context.Background(), msg); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	return got
}

// A time-sensitive notification is the one a Focus mode does not hold back. Only the two
// operational alerts ask for it: a comment can wait until the reader looks at their phone.
func TestBarkAsksForTimeSensitiveOnlyOnAnAlert(t *testing.T) {
	t.Setenv("BASE_URL", "https://probe.example")

	if got := barkPayload(t, Message{Subject: "s", Alert: true}); got["level"] != "timeSensitive" {
		t.Errorf("level = %v on an alert, want timeSensitive", got["level"])
	}
	if got := barkPayload(t, Message{Subject: "s"}); got["level"] != nil {
		t.Errorf("level = %v on an ordinary message, want the field left out", got["level"])
	}
}

// A push carries a link only when the message has one. The two failure alerts have no single page
// to open, and a notification that opens nothing is worse than one that opens nothing on purpose.
func TestBarkCarriesALinkOnlyWhenTheMessageHasOne(t *testing.T) {
	t.Setenv("BASE_URL", "https://probe.example")

	got := barkPayload(t, Message{Subject: "s", Link: "https://probe.example/post/1"})
	if got["url"] != "https://probe.example/post/1" {
		t.Errorf("url = %v, want the message's link", got["url"])
	}
	if got := barkPayload(t, Message{Subject: "s"}); got["url"] != nil {
		t.Errorf("url = %v on a message with no link, want the field left out", got["url"])
	}
}

// The two descriptive fields follow the site's own configuration: the subtitle is the site's
// name, the group is the domain it is served from. NOTIFY_BARK_GROUP overrides the filing name
// only, not what the notification says about where it came from.
func TestBarkDescribesTheSiteItCameFrom(t *testing.T) {
	t.Setenv("BASE_URL", "https://probe.example")

	got := barkPayload(t, Message{Subject: "s"})
	if got["subtitle"] != siteName() {
		t.Errorf("subtitle = %v, want %q", got["subtitle"], siteName())
	}
	if got["group"] != "probe.example" {
		t.Errorf("group = %v, want the sending domain", got["group"])
	}

	t.Setenv("NOTIFY_BARK_GROUP", "mine")
	got = barkPayload(t, Message{Subject: "s"})
	if got["group"] != "mine" {
		t.Errorf("group = %v, want the override", got["group"])
	}
	if got["subtitle"] != siteName() {
		t.Errorf("subtitle = %v, want %q even under a group override", got["subtitle"], siteName())
	}
}

// The notification icon is decoded by the platform's image loader, which has no SVG decoder, so
// the SVG favicon a site serves its browser cannot be used. A site that ships neither bitmap
// sends no icon at all, because /favicon.ico answers with the SVG bytes when there is no file
// behind it. The ICO wins over the PNG: it is the file the app renders.
func TestBarkSendsOnlyAnIconTheAppCanDecode(t *testing.T) {
	t.Setenv("BASE_URL", "https://probe.example")
	useExampleSite(t)

	if got := barkPayload(t, Message{Subject: "s"}); got["icon"] != nil {
		t.Errorf("icon = %v on a site with no bitmap, want the field left out", got["icon"])
	}

	// Only the files' presence is under test here, not their contents: what the channel chooses
	// is the URL, and the bytes behind it are the site's business.
	ship := func(name string) {
		t.Helper()
		if err := os.MkdirAll(RootPublic, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", RootPublic, err)
		}
		if err := os.WriteFile(filepath.Join(RootPublic, name), []byte("bitmap"), 0o644); err != nil {
			t.Fatalf("write the site's %s: %v", name, err)
		}
	}

	ship("favicon.png")
	if got := barkPayload(t, Message{Subject: "s"}); got["icon"] != "https://probe.example/favicon.png" {
		t.Errorf("icon = %v, want the site's PNG when it ships no ICO", got["icon"])
	}

	ship("favicon.ico")
	if got := barkPayload(t, Message{Subject: "s"}); got["icon"] != "https://probe.example"+RouteFaviconIco {
		t.Errorf("icon = %v, want the ICO in preference to the PNG", got["icon"])
	}
}

// The test button sends the event's own message with placeholder data, so what a channel receives
// is the shape of the notification it will one day carry.
func TestEverySampleMessageMatchesItsEvent(t *testing.T) {
	t.Setenv("BASE_URL", "https://probe.example")

	withLink := map[string]bool{EventCommentNew: true, EventFriendApply: true, EventStickerApply: true}
	for _, e := range notifyEvents {
		msg, ok := sampleMessageFor(e.Event)
		if e.Visitor {
			if ok {
				t.Errorf("%s reaches a visitor, so no sample can stand in for it", e.Event)
			}
			continue
		}
		if !ok {
			t.Errorf("%s has no sample message, so the panel offers a button that cannot work", e.Event)
			continue
		}
		if msg.Event != e.Event {
			t.Errorf("the sample for %s carries event %q", e.Event, msg.Event)
		}
		if msg.Subject == "" || msg.Body == "" || msg.Chat == "" {
			t.Errorf("the sample for %s is missing a part: subject=%q body=%q chat=%q",
				e.Event, msg.Subject, msg.Body, msg.Chat)
		}
		wantLink := withLink[e.Event]
		if (msg.Link != "") != wantLink {
			t.Errorf("%s carries link %q; a link is wanted: %v", e.Event, msg.Link, wantLink)
		}
		// The link is a field of its own, so a chat channel that can carry one uses that and
		// the text does not repeat it. bark shows both, and two tap targets for one page is
		// one too many.
		if msg.Link != "" && strings.Contains(msg.Chat, msg.Link) {
			t.Errorf("%s repeats its link in the chat text: %q", e.Event, msg.Chat)
		}
		if msg.Alert != !wantLink {
			t.Errorf("%s has Alert=%v; only the two operational alerts ask for it", e.Event, msg.Alert)
		}
	}
}

// The concise form is the whole point of it: the mail version's rules, greetings and sign-offs
// are what an operator on a phone has to read past to find the one line that matters.
func TestTheChatFormDropsTheMailDecoration(t *testing.T) {
	t.Setenv("BASE_URL", "https://probe.example")

	for _, e := range notifyEvents {
		msg, ok := sampleMessageFor(e.Event)
		if !ok {
			continue
		}
		for _, banned := range []string{
			"----", "站长你好", "本邮件", "直达链接", "查看友链页面", "前往后台",
			"临时 SVG 路径", "暂存目录",
		} {
			if strings.Contains(msg.Chat, banned) {
				t.Errorf("%s: the chat form carries %q: %s", e.Event, banned, msg.Chat)
			}
		}
	}
}

// A failing channel reports its failure rather than swallowing it; the caller decides what to do
// with it, and for the invalidation path that decision is "log it and move on".
func TestAFailingChannelReportsIt(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusInternalServerError)
	}))
	defer srv.Close()

	t.Setenv("NOTIFY_WEBHOOK_URL", srv.URL)
	if err := newWebhookChannel().Deliver(context.Background(), Message{Subject: "s"}); err == nil {
		t.Error("a 500 from the far side was reported as success")
	}
}

// The administrator's page renders these two rows, and a page's data has to be a pure function of
// the environment — not of when the page was rendered.
//
// If the rows waited for initNotifiers(), the startup warm would render the "无" defaults and the
// next render would show "resend": /dashboard's bytes would change twice per restart and it would
// be purged each time — the diff would be exactly this pair of rows.
//
// Deliberately does not call initNotifiers(): this is the state the startup warm renders in.
func TestNotificationRowsDoNotWaitForInit(t *testing.T) {
	t.Setenv(envNotifyChannels, "resend")
	t.Setenv("NOTIFY_RESEND_API_KEY", "key")
	t.Setenv(envNotifyAdminVia, "")
	t.Setenv(envNotifyVisitorVia, "")

	admin, visitor := notifyRows()
	if len(admin) == 0 || admin[0].Name != "resend" || !admin[0].Active {
		t.Errorf("administrator row before initNotifiers() is %v, want resend first and active", admin)
	}
	if len(visitor) == 0 || visitor[0].Name != "resend" || !visitor[0].Active {
		t.Errorf("visitor row before initNotifiers() is %v, want resend first and active", visitor)
	}

	// The channel list on the same page comes from the same resolution and must agree.
	var resend *channelStatus
	status := channelStatuses(resolveNotifiers())
	for i := range status {
		if st := status[i]; st.Name == "resend" {
			resend = &st
		}
	}
	if resend == nil {
		t.Fatal("resend is missing from the channel list")
	}
	if !resend.Set || resend.Kind != KindMail {
		t.Errorf("resend: Set=%v Kind=%q, want a set mail channel",
			resend.Set, resend.Kind)
	}
}

// An administrator notification carries no address of its own, so a mail channel resolves one
// from the mail layer. Handing it in from the caller instead is what made one mail variable the
// gate on every channel.
func TestAMailChannelResolvesTheAdministratorAddressItself(t *testing.T) {
	t.Setenv(envMailTo, "admin@example.com")

	resend := &recordingChannel{name: "resend", kind: KindMail}
	chat := &recordingChannel{name: "chat"}
	saved := notifiers
	notifiers = &notifierSet{
		byName: map[string]Notifier{"resend": resend, "chat": chat},
		admin:  []Notifier{resend, chat},
	}
	t.Cleanup(func() { notifiers = saved })

	notifyAdmin(Message{Event: EventCommentNew, Subject: "s", Body: "b"})

	if len(resend.got) != 1 || len(chat.got) != 1 {
		t.Fatalf("reached resend %d time(s) and chat %d time(s), want one each",
			len(resend.got), len(chat.got))
	}
	if got := mailRecipient(resend.got[0]); got != "admin@example.com" {
		t.Errorf("the mail channel would deliver to %q, want the configured administrator address", got)
	}
	// A visitor's own address wins when the caller supplied one, or a reply would go to the
	// operator instead of to the visitor.
	if got := mailRecipient(Message{To: "visitor@example.com"}); got != "visitor@example.com" {
		t.Errorf("mailRecipient = %q, want the address the caller supplied", got)
	}
}

// A node that has set no mail address still reaches its chat channels. The guard this replaces
// read the mail address inside the caller and returned before notifyAdmin, which silenced every
// other channel on that node — the one failure a notification setting must not have.
func TestAChatChannelIsReachedWithNoMailAddressConfigured(t *testing.T) {
	useExampleSite(t)
	initEmailTemplates()
	t.Setenv(envMailTo, "")

	chat := &recordingChannel{name: "chat"}
	saved := notifiers
	notifiers = &notifierSet{byName: map[string]Notifier{"chat": chat}, admin: []Notifier{chat}}
	t.Cleanup(func() { notifiers = saved })

	sendCommentNotify("小明", "你好", "随笔", "https://example.com/post/1")

	if len(chat.got) != 1 {
		t.Fatalf("the chat channel received %d message(s), want 1: an address this node does not "+
			"have must not decide whether any other channel is reached", len(chat.got))
	}
	if got := chat.got[0].Event; got != EventCommentNew {
		t.Errorf("the message carries event %q, want %q", got, EventCommentNew)
	}
}
