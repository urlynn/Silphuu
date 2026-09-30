package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// The channels that are not mail. Each is one POST; the shape of the request is the only thing
// that differs, and each one's shape is that service's own rule — which is why it lives here
// and not in the code that decides a message has to go out.

// Telegram.

type telegramChannel struct {
	token  string
	chatID string
}

func newTelegramChannel() Notifier {
	token := strings.TrimSpace(os.Getenv("NOTIFY_TELEGRAM_BOT_TOKEN"))
	chatID := strings.TrimSpace(os.Getenv("NOTIFY_TELEGRAM_CHAT_ID"))
	if token == "" || chatID == "" {
		return nil
	}
	return &telegramChannel{token: token, chatID: chatID}
}

func (c *telegramChannel) Name() string     { return "telegram" }
func (c *telegramChannel) Kind() string     { return "" }
func (c *telegramChannel) Endpoint() string { return c.chatID }

func (c *telegramChannel) Deliver(ctx context.Context, msg Message) error {
	text := msg.Subject
	if body := msg.renderChat(); body != "" {
		text += "\n\n" + body
	}
	return postJSON(ctx,
		fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", c.token),
		nil,
		map[string]any{"chat_id": c.chatID, "text": text, "disable_web_page_preview": true})
}

// Gotify.

type gotifyChannel struct {
	url   string
	token string
}

func newGotifyChannel() Notifier {
	url := strings.TrimRight(strings.TrimSpace(os.Getenv("NOTIFY_GOTIFY_URL")), "/")
	token := strings.TrimSpace(os.Getenv("NOTIFY_GOTIFY_TOKEN"))
	if url == "" || token == "" {
		return nil
	}
	return &gotifyChannel{url: url, token: token}
}

func (c *gotifyChannel) Name() string     { return "gotify" }
func (c *gotifyChannel) Kind() string     { return "" }
func (c *gotifyChannel) Endpoint() string { return c.url }

func (c *gotifyChannel) Deliver(ctx context.Context, msg Message) error {
	return postJSON(ctx,
		fmt.Sprintf("%s/message?token=%s", c.url, c.token),
		nil,
		map[string]any{"title": msg.Subject, "message": msg.renderChat(), "priority": 5})
}

// Bark.

type barkChannel struct {
	url string
	key string
	// group is what the notification is filed under in the app. It is resolved once, at
	// construction, because it follows the site's own configuration, which is not settled at
	// package init.
	group string
	// subtitle is the site's own name, shown under the title; the domain is what group carries.
	// icon is the site's own icon beside the text. Both follow the site's configuration.
	subtitle string
	icon     string
	// ttl is how long the app keeps the notification in its history, in seconds; 0 asks for no
	// expiry. It bounds that record and the entry in Notification Center, not the delivery.
	ttl int
}

// newBarkChannel builds the channel from NOTIFY_BARK_KEY and, optionally, NOTIFY_BARK_URL for a
// self-hosted server. Without a key there is no device to push to.
func newBarkChannel() Notifier {
	url := strings.TrimRight(strings.TrimSpace(os.Getenv("NOTIFY_BARK_URL")), "/")
	key := strings.TrimSpace(os.Getenv("NOTIFY_BARK_KEY"))
	if key == "" {
		return nil
	}
	if url == "" {
		url = "https://api.day.app"
	}
	return &barkChannel{
		url:      url,
		key:      key,
		group:    barkGroup(),
		subtitle: siteName(),
		icon:     barkIcon(),
		ttl:      barkTTL(),
	}
}

func (c *barkChannel) Name() string { return "bark" }
func (c *barkChannel) Kind() string { return "" }

// Endpoint is the server, never the key. The key travels in the request body rather than in the
// path, so a transport failure — whose error quotes the URL — does not write it to the log.
func (c *barkChannel) Endpoint() string { return c.url }

// barkGroup is the group a notification is filed under: the domain the sending node serves. It
// comes from the site's configuration rather than from a literal, so the two nodes' notices are
// told apart by the domain that sent them. NOTIFY_BARK_GROUP overrides it, for a deployment that
// wants its own filing.
func barkGroup() string {
	if g := strings.TrimSpace(os.Getenv("NOTIFY_BARK_GROUP")); g != "" {
		return g
	}
	return siteHost()
}

// barkIcon is the site's own icon, as the notification shows it, or "" when the site ships none
// the app can use.
//
// It has to be a bitmap and not the SVG favicon the same site serves its browser: the icon is
// decoded by the platform's image loader, which has no SVG decoder. The ICO comes before the PNG
// because it is the file the app renders. /favicon.ico answers with the SVG bytes when a site has
// no ICO of its own, so each file has to be checked rather than its URL assumed.
func barkIcon() string {
	for _, name := range []string{"favicon.ico", "favicon.png"} {
		if _, err := os.Stat(filepath.Join(RootPublic, name)); err == nil {
			return siteBaseURL() + "/" + name
		}
	}
	return ""
}

// barkTTL is how long the app keeps this notification in its history, in seconds, or 0 to ask for
// no expiry. The retention bounds the saved record and the entry in Notification Center, not the
// delivery: the push arrives either way. A value that is not a positive number is reported and
// then ignored, because a notification with no expiry still arrives and one that is never sent
// does not.
func barkTTL() int {
	raw := strings.TrimSpace(os.Getenv("NOTIFY_BARK_TTL"))
	if raw == "" {
		return 0
	}
	seconds, err := strconv.Atoi(raw)
	if err != nil || seconds <= 0 {
		log.Printf("notify: NOTIFY_BARK_TTL=%q is not a positive number of seconds; no expiry is asked for", raw)
		return 0
	}
	return seconds
}

func (c *barkChannel) Deliver(ctx context.Context, msg Message) error {
	payload := map[string]any{
		"device_key": c.key,
		"title":      msg.Subject,
		"subtitle":   c.subtitle,
		"body":       msg.renderChat(),
		"group":      c.group,
	}
	if c.ttl > 0 {
		payload["ttl"] = c.ttl
	}
	if c.icon != "" {
		payload["icon"] = c.icon
	}
	// A link is only worth carrying when the message has one: two of the events report a
	// failure rather than a page, and a push that opens nothing is a push that lies. The chat
	// text does not repeat it — the app turns this field into the one tap target.
	if msg.Link != "" {
		payload["url"] = msg.Link
	}
	// timeSensitive lets an operational failure through a Focus mode. It is not asked for by
	// the content events, which can wait.
	if msg.Alert {
		payload["level"] = "timeSensitive"
	}
	return postJSON(ctx, c.url+"/push", nil, payload)
}

// Any HTTP endpoint.

type webhookChannel struct{ url string }

func newWebhookChannel() Notifier {
	url := strings.TrimSpace(os.Getenv("NOTIFY_WEBHOOK_URL"))
	if url == "" {
		return nil
	}
	return &webhookChannel{url: url}
}

func (c *webhookChannel) Name() string     { return "webhook" }
func (c *webhookChannel) Kind() string     { return "" }
func (c *webhookChannel) Endpoint() string { return c.url }

// Deliver posts the message as JSON. The body is the whole Message, so an endpoint on the other
// side can decide for itself what to do with the fields rather than parsing them back out of a
// sentence. Event is a key of its own rather than one more field: a receiver branches on it, and
// fields is content.
//
// text keeps its meaning — the mail text with its fields — because a receiver parses it. chat and
// link are the chat channels' concise form, offered alongside rather than replacing it.
func (c *webhookChannel) Deliver(ctx context.Context, msg Message) error {
	return postJSON(ctx, c.url, nil, map[string]any{
		"event":   msg.Event,
		"subject": msg.Subject,
		"body":    msg.Body,
		"text":    msg.renderText(),
		"chat":    msg.Chat,
		"link":    msg.Link,
		"fields":  msg.Fields,
	})
}
