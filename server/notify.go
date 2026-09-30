package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/mail"
	"os"
	"sort"
	"strings"
	"time"
)

// Notification: one interface, several channels, two kinds of recipient.
//
// Which service carries a message is the deployment's own business — the same rule as the
// invalidation outlet — so a channel is a named implementation chosen by environment variable,
// and the engine knows nothing else about it.
//
// Two kinds of recipient, and they are not interchangeable:
//
//   - A visitor notification goes to an address the visitor typed into a comment form. Only a
//     channel that delivers to an address it was handed can do that, which is why the visitor
//     set is restricted to KindMail channels.
//   - An administrator notification goes to the operator, who chooses how they want to be
//     reached: mail, a chat channel, a webhook.
//
// Configuration comes from the environment and, for routing, from the settings directory
// (SILPHUU_SETTINGS_DIR). Nothing about a deployment's notification belongs in the site's json:
// the channels a server can send on are that server's own, and so is the choice of which of
// them carries which message.
//
//	NOTIFY_CHANNELS         comma-separated, the channels that exist on this node
//	NOTIFY_ADMIN_VIA        which of them the administrator is reached on (default: all)
//	NOTIFY_VISITOR_VIA      which of them visitors are reached on (default: the mail ones)
//
//	NOTIFY_RESEND_API_KEY   resend
//	NOTIFY_SMTP_ADDR, NOTIFY_SMTP_USER, NOTIFY_SMTP_PASS   smtp, on a submission port
//	NOTIFY_MAIL_FROM        the From header; default is derived from the site
//	NOTIFY_MAIL_TO          where an administrator notification is mailed; no default
//	NOTIFY_TELEGRAM_BOT_TOKEN, NOTIFY_TELEGRAM_CHAT_ID   telegram
//	NOTIFY_GOTIFY_URL, NOTIFY_GOTIFY_TOKEN               gotify
//	NOTIFY_BARK_KEY, NOTIFY_BARK_URL                     bark (URL defaults to api.day.app)
//	NOTIFY_BARK_GROUP, NOTIFY_BARK_TTL                   bark; group defaults to site_name@domain
//	NOTIFY_WEBHOOK_URL      webhook
//
// The two _VIA variables are the audience default. A route recorded for an event replaces it
// for that event alone; a deployment that has recorded none — which is every deployment that
// has never opened the settings panel — is reached exactly as the two variables say. See
// notify_routes.go for why a route is a file rather than another variable.
const (
	envNotifyChannels   = "NOTIFY_CHANNELS"
	envNotifyAdminVia   = "NOTIFY_ADMIN_VIA"
	envNotifyVisitorVia = "NOTIFY_VISITOR_VIA"
)

// KindMail marks a channel that delivers to an address the caller supplies. Only these may
// carry a visitor notification.
const KindMail = "mail"

// The kinds of message this build sends, which is what a route names. Every value is a
// dotted pair so a new one can be added without colliding with an existing name.
const (
	EventCommentNew       = "comment.new"
	EventCommentReply     = "comment.reply"
	EventFriendApply      = "friend.apply"
	EventStickerApply     = "sticker.apply"
	EventInvalidateFailed = "invalidate.failed"
	EventBlobFailed       = "blob.failed"
)

// notifyEvent is one kind of message, as the settings panel lists it.
//
// Visitor marks the one whose recipient is an address a visitor typed into a form. Only a
// mail channel can deliver to an address it was handed, so that event's row offers nothing
// else — see visitorOnly.
type notifyEvent struct {
	Event   string
	Visitor bool
}

// notifyEvents is every kind this build routes, in the order the settings panel lists them. The
// visitor event leads, because it is the only row restricted to mail channels.
var notifyEvents = []notifyEvent{
	{Event: EventCommentReply, Visitor: true},
	{Event: EventCommentNew},
	{Event: EventFriendApply},
	{Event: EventStickerApply},
	{Event: EventInvalidateFailed},
	{Event: EventBlobFailed},
}

// notifyEventNamed returns the event with this name, if this build sends it. A name that is not
// in the table is not an event, which is how a caller refuses one.
func notifyEventNamed(name string) (notifyEvent, bool) {
	for _, e := range notifyEvents {
		if e.Event == name {
			return e, true
		}
	}
	return notifyEvent{}, false
}

// Message is what every channel receives. A mail channel sends HTML with Body as its plain-text
// alternative; a chat channel sends Subject as the title and renderChat() as the text; the
// webhook posts a chosen set of these fields.
type Message struct {
	// Event names the kind of message, which is what a route keys on. It is metadata, not
	// content: unlike Fields it is not rendered into the text form.
	Event   string
	Subject string
	// Body is the plain-text part of a mail message.
	Body string
	// HTML is the HTML part of a mail message.
	HTML string
	// Chat is the text a chat channel shows. Empty falls back to the mail text with its fields.
	Chat string
	// Fields are the machine-readable details. A chat channel shows them only through the
	// fallback in renderChat, so a message with a Chat does not have to repeat them there.
	Fields map[string]string
	// Link is where a chat channel can send a reader who wants the full page. A chat channel
	// that has no way to carry a link ignores it.
	Link string
	// Alert marks an operational failure, which a chat channel may deliver more urgently.
	Alert bool
	// To is the recipient for a visitor notification. Empty means "the administrator's own
	// channels", which know their own destination.
	To string
}

// Notifier delivers one message over one channel.
type Notifier interface {
	Name() string
	Kind() string
	// Endpoint is the value this node configured for the channel — the address mail goes out from,
	// the chat a notice lands in, the URL that is called. It is shown on the administrator's page,
	// so it is never a credential: a key on a page is a key in a screenshot.
	Endpoint() string
	Deliver(ctx context.Context, msg Message) error
}

// bareAddress is the address out of a From value, which may carry a display name.
func bareAddress(from string) string {
	if a, err := mail.ParseAddress(from); err == nil {
		return a.Address
	}
	return from
}

// notifyHTTPTimeout bounds one delivery. A channel that hangs would otherwise hold the caller —
// and for the invalidation path that caller is the flush, which must not stall on a chat API.
const notifyHTTPTimeout = 10 * time.Second

// notifiers is the node's channel set. It is built by initNotifiers() from main(), not at
// package initialisation: the mail channel's From header is derived from the site's config, and
// at package-init time the roots still point at their defaults, so it would read the wrong
// site.json.
var notifiers *notifierSet

func initNotifiers() { notifiers = loadNotifiers() }

type notifierSet struct {
	byName  map[string]Notifier
	enabled []string // what NOTIFY_CHANNELS named, in order
	admin   []Notifier
	visitor []Notifier
	// routes is the recorded routing, by event. Absent means the audience default; present
	// and empty means the event is switched off.
	routes map[string][]string
}

// notifyAdmin sends to the administrator over every channel this message's event routes to.
// It never returns an error: a notification that fails is logged and forgotten, because the
// alternative — reporting a failed notification to whoever was reporting a failure — is a
// loop with no floor.
func notifyAdmin(msg Message) {
	if notifiers == nil {
		return
	}
	for _, n := range notifiers.channelsFor(msg.Event, notifiers.admin) {
		deliver(n, msg)
	}
}

// notifyVisitor sends to a visitor's own address. A node with no mail channel configured simply
// does not reach them; that is a deployment choice, not an error.
func notifyVisitor(msg Message) {
	if notifiers == nil || strings.TrimSpace(msg.To) == "" {
		return
	}
	// A route can name a channel that cannot deliver to an address it was handed, so the
	// mail-only rule is applied to the result and not only to the default. A visitor's
	// message must never land in the operator's own chat because a table said so.
	routed := notifiers.channelsFor(msg.Event, notifiers.visitor)
	for _, n := range notifiers.visitorOnly(routed) {
		deliver(n, msg)
	}
}

// channelsFor returns the channels one event goes out on. A recorded route wins; with none the
// audience default applies. A route can only name a channel this node holds credentials for —
// one it does not is dropped, and loadNotifiers reports it once at startup rather than on
// every render.
func (s *notifierSet) channelsFor(evt string, fallback []Notifier) []Notifier {
	routed, ok := s.routes[evt]
	if !ok {
		return fallback
	}
	var out []Notifier
	for _, name := range routed {
		if n, found := s.byName[name]; found {
			out = append(out, n)
		}
	}
	return out
}

func deliver(n Notifier, msg Message) {
	ctx, cancel := context.WithTimeout(context.Background(), notifyHTTPTimeout)
	defer cancel()
	deliverCtx(ctx, n, msg)
}

// deliverCtx is deliver under a caller's context, so several channels can be delivered under one
// deadline. It never returns an error: see notifyAdmin.
func deliverCtx(ctx context.Context, n Notifier, msg Message) {
	if err := n.Deliver(ctx, msg); err != nil {
		log.Printf("notify: %s could not deliver %q: %v", n.Name(), msg.Subject, err)
	}
}

// resolveNotifiers builds the set from the environment and the routing file. It logs nothing.
//
// It is separate from loadNotifiers because the administrator's page renders the routing, and a
// page's data must be a pure function of the configuration — not of when the page was rendered.
// If this waited for initNotifiers(), the startup warm would render the "无" defaults and the next
// render would show "resend": /dashboard's bytes would change twice per restart and it would be
// purged each time.
func resolveNotifiers() *notifierSet {
	set := &notifierSet{byName: map[string]Notifier{}, routes: loadNotifyRoutes()}
	for _, n := range []Notifier{
		newSMTPChannel(),
		newResendChannel(),
		newBarkChannel(),
		newGotifyChannel(),
		newWebhookChannel(),
		newTelegramChannel(),
	} {
		if n != nil {
			set.byName[n.Name()] = n
		}
	}

	enabled := splitList(os.Getenv(envNotifyChannels))
	set.enabled = enabled
	set.admin = set.pick(enabled, os.Getenv(envNotifyAdminVia))

	visitorVia := os.Getenv(envNotifyVisitorVia)
	if strings.TrimSpace(visitorVia) == "" {
		// Default: every enabled channel that can reach an address it was handed.
		var mail []string
		for _, name := range enabled {
			if n, ok := set.byName[name]; ok && n.Kind() == KindMail {
				mail = append(mail, name)
			}
		}
		visitorVia = strings.Join(mail, ",")
	}
	set.visitor = set.visitorOnly(set.pick(enabled, visitorVia))
	return set
}

// loadNotifiers is the startup form of resolveNotifiers: it resolves the set and reports what it
// found, once.
//
// An unknown name in NOTIFY_CHANNELS is logged and skipped rather than fatal: a typo in one
// channel's name must not stop the node from telling the operator about anything at all. A route
// that names an unknown channel is reported here for the same reason — the failure it causes is
// silence, which nothing else would explain.
func loadNotifiers() *notifierSet {
	set := resolveNotifiers()
	for _, name := range set.enabled {
		if _, ok := set.byName[name]; !ok {
			log.Printf("notify: %s names %q, which this build has no channel for", envNotifyChannels, name)
		}
	}
	for _, e := range notifyEvents {
		for _, name := range set.routes[e.Event] {
			if _, ok := set.byName[name]; !ok {
				log.Printf("notify: the route for %s names %q, which this node has no channel for; it is skipped", e.Event, name)
			}
		}
	}
	if names := notifierNames(set.admin); len(names) > 0 {
		log.Printf("notify: administrator reached via %s", strings.Join(names, ", "))
	}
	if names := notifierNames(set.visitor); len(names) > 0 {
		log.Printf("notify: visitors reached via %s", strings.Join(names, ", "))
	}
	// A mail channel with no administrator address configured cannot carry an administrator
	// notification, and that failure is a delivery which never happens. Report it once here
	// rather than let it read as a quiet day.
	if notifyMailTo() == "" {
		var mail []string
		for name, n := range set.byName {
			if n.Kind() == KindMail {
				mail = append(mail, name)
			}
		}
		sortChannels(mail)
		if len(mail) > 0 {
			log.Printf("notify: %s is not set, so the mail channel(s) %s cannot carry an administrator notification",
				envMailTo, strings.Join(mail, ", "))
		}
	}
	return set
}

// pick resolves a name list against the enabled set. Visitor routing additionally drops any
// channel that cannot deliver to an address it was handed — a chat channel in
// NOTIFY_VISITOR_VIA is a configuration mistake, and silently sending the visitor's message to
// the operator's own chat instead would be worse than not sending it.
func (s *notifierSet) pick(enabled []string, names string) []Notifier {
	want := splitList(names)
	if len(want) == 0 {
		want = enabled
	}
	var out []Notifier
	for _, name := range want {
		n, ok := s.byName[name]
		if !ok {
			continue
		}
		out = append(out, n)
	}
	return out
}

// visitorNotifiers drops the channels that cannot carry a visitor notification.
func (s *notifierSet) visitorOnly(list []Notifier) []Notifier {
	var out []Notifier
	for _, n := range list {
		if n.Kind() == KindMail {
			out = append(out, n)
		}
	}
	return out
}

// channelStatus is what the administrator's page knows about one channel: the kind it reports,
// whether this node holds the credentials to build it, and the endpoint those credentials name.
// The endpoint is an address or a URL, never a key — see Notifier.Endpoint.
type channelStatus struct {
	Name     string
	Set      bool
	Kind     string
	Endpoint string
}

// knownChannels is every channel this build knows, in the order the panel, the routing file and
// the startup log present them. The order is the product's, not the alphabet's.
//
// The kind is static here because the panel has to say whether a channel could carry a visitor's
// message even on a node that holds no credentials for it, and Kind() only answers from a built
// channel.
var knownChannels = []channelStatus{
	{Name: "smtp", Kind: KindMail},
	{Name: "resend", Kind: KindMail},
	{Name: "bark"},
	{Name: "gotify"},
	{Name: "webhook"},
	{Name: "telegram"},
}

// channelRank is a name's position in knownChannels. A name this build does not know — a typo in
// NOTIFY_CHANNELS — ranks after every known one.
func channelRank(name string) int {
	for i, k := range knownChannels {
		if k.Name == name {
			return i
		}
	}
	return len(knownChannels)
}

// sortChannels puts names in knownChannels order, alphabetically among names of equal rank.
func sortChannels(names []string) {
	sort.SliceStable(names, func(i, j int) bool {
		ri, rj := channelRank(names[i]), channelRank(names[j])
		if ri != rj {
			return ri < rj
		}
		return names[i] < names[j]
	})
}

// channelStatuses reports every channel this build knows, in a stable order, for a set that is
// already resolved — a caller that needs the rows and the statuses both reads the environment
// once. The statuses come from that resolution rather than from the startup set, so the page's
// data does not depend on when the page was rendered. See resolveNotifiers.
func channelStatuses(set *notifierSet) []channelStatus {
	out := make([]channelStatus, 0, len(knownChannels))
	for _, k := range knownChannels {
		st := k
		if n, ok := set.byName[k.Name]; ok {
			st.Set = true
			st.Endpoint = n.Endpoint()
		}
		out = append(out, st)
	}
	return out
}

// notifierNames is the routing, for the page and for the startup log.
func notifierNames(list []Notifier) []string {
	out := make([]string, 0, len(list))
	for _, n := range list {
		out = append(out, n.Name())
	}
	return out
}

// notifyBadge is one channel chip on the notification panel. Active marks a channel the row's
// recipient is reached on; the rest are the channels this build knows, shown after it.
type notifyBadge struct {
	Name   string
	Active bool
	// Value is the address or endpoint the row shows after the channel name. Empty for a channel
	// this node holds no credentials for: a value there would describe a delivery that cannot
	// happen.
	Value string
}

// notifyRowFor is one row of the panel: the channels that routing uses, then the channels this
// build knows that the routing could use, the configured ones before the unconfigured.
//
// visitors selects the visitor audience. Its row may only name mail channels, because a visitor's
// address is typed into a comment form and a chat channel has no way to reach them — naming one
// there would offer an operator something the routing cannot do.
func notifyRowFor(status []channelStatus, active []string, visitors bool) []notifyBadge {
	used := map[string]bool{}
	for _, name := range active {
		used[name] = true
	}

	var on, configured, absent []notifyBadge
	for _, st := range status {
		if visitors && st.Kind != KindMail {
			continue
		}
		b := notifyBadge{Name: st.Name, Active: used[st.Name]}
		if st.Set {
			b.Value = notifyBadgeValue(st, visitors)
		}
		switch {
		case b.Active:
			on = append(on, b)
		case st.Set:
			configured = append(configured, b)
		default:
			absent = append(absent, b)
		}
	}
	return append(append(on, configured...), absent...)
}

// notifyBadgeValue is what a badge shows after the channel name.
//
// A visitor typed their own address into the comment form, so the only address this node owns on
// that row is the one the message comes from. The administrator's row says where the message goes.
// A chat or webhook channel has no recipient of its own on either row, so both show its endpoint.
func notifyBadgeValue(st channelStatus, visitors bool) string {
	if !visitors && st.Kind == KindMail {
		return notifyMailTo()
	}
	return st.Endpoint
}

// notifyRows is the panel's two rows, resolved from the environment on every call.
func notifyRows() (admin, visitor []notifyBadge) {
	set := resolveNotifiers()
	status := channelStatuses(set)
	return notifyRowFor(status, notifierNames(set.admin), false),
		notifyRowFor(status, notifierNames(set.visitor), true)
}

// notifyRouteOption is one channel a route can name, as the settings panel offers it.
//
// Set is whether this node holds the credentials to build the channel: one without them cannot
// carry anything, so the panel shows it rather than offering it.
type notifyRouteOption struct {
	Name    string
	Set     bool
	Checked bool
}

// notifyRouteGroup is one event's block in the settings panel. Label is the event's name in the
// reader's language; Event stays visible beside it, because a route that names an event is
// something an operator matches against the file when a notification does not arrive.
type notifyRouteGroup struct {
	Event   string
	Label   string
	Visitor bool
	Options []notifyRouteOption
}

// notifyRouteGroups is the settings panel's content, resolved on every call — the same rule as
// notifyRows, and for the same reason.
func notifyRouteGroups() []notifyRouteGroup {
	set := resolveNotifiers()
	status := channelStatuses(set)
	groups := make([]notifyRouteGroup, 0, len(notifyEvents))
	for _, e := range notifyEvents {
		fallback := set.admin
		if e.Visitor {
			fallback = set.visitor
		}
		active := map[string]bool{}
		for _, n := range set.channelsFor(e.Event, fallback) {
			active[n.Name()] = true
		}
		g := notifyRouteGroup{
			Event:   e.Event,
			Label:   uiText("admin.notify.event." + strings.ReplaceAll(e.Event, ".", "_")),
			Visitor: e.Visitor,
		}
		for _, st := range status {
			// A visitor's event takes mail channels only, which is the rule the routing
			// applies too: an option that could not be saved must not be offered.
			if e.Visitor && st.Kind != KindMail {
				continue
			}
			g.Options = append(g.Options, notifyRouteOption{
				Name:    st.Name,
				Set:     st.Set,
				Checked: active[st.Name],
			})
		}
		groups = append(groups, g)
	}
	return groups
}

// splitList reads a comma-separated list of channel names. The result is in knownChannels order,
// so the startup log and the routing file read the same way the panel does.
func splitList(raw string) []string {
	var out []string
	for _, part := range strings.Split(raw, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	sortChannels(out)
	return out
}

// renderText is the plain-text form a chat channel or a webhook gets: the body, then the fields
// as one "key: value" per line. Stable order, so a diff of two notifications reads.
func (m Message) renderText() string {
	var b strings.Builder
	if m.Body != "" {
		b.WriteString(m.Body)
		b.WriteString("\n")
	}
	keys := make([]string, 0, len(m.Fields))
	for k := range m.Fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(&b, "%s: %s\n", k, m.Fields[k])
	}
	return strings.TrimRight(b.String(), "\n")
}

// renderChat is what a chat channel shows: the concise form when the message carries one, else
// the mail text with its fields.
func (m Message) renderChat() string {
	if m.Chat != "" {
		return m.Chat
	}
	return m.renderText()
}

// mailHeader is one header line of an outgoing message, named the way the wire carries it.
type mailHeader struct{ name, value string }

// mailHeaders are the headers every outgoing message carries, in a fixed order: a visitor who
// asked to be replied to has to be able to stop the replies, and an automated message has to say
// so to the receiving side. A property of mail, not of any one channel or any one message.
func mailHeaders() []mailHeader {
	return []mailHeader{
		{"List-Unsubscribe", fmt.Sprintf("<mailto:noreply@%s?subject=unsubscribe>, <%s>", siteHost(), siteBaseURL())},
		{"List-Unsubscribe-Post", "List-Unsubscribe=One-Click"},
		{"Auto-Submitted", "auto-generated"},
		{"X-Auto-Response-Suppress", "All"},
	}
}

// postJSON sends body to url with the given headers. Every channel that is not mail is this.
func postJSON(ctx context.Context, url string, headers map[string]string, body any) error {
	data, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := (&http.Client{Timeout: notifyHTTPTimeout}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		var buf bytes.Buffer
		_, _ = buf.ReadFrom(resp.Body)
		return fmt.Errorf("status %d: %s", resp.StatusCode, strings.TrimSpace(buf.String()))
	}
	return nil
}
