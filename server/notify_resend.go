package main

import (
	"context"
	"fmt"
	"os"
	"strings"
)

// The mail channel over an HTTP mail service.
//
// A mail channel is the only kind that can deliver to an address the caller hands over — which
// is exactly what a visitor notification needs, because that address is one the visitor typed.
// Every other channel reaches its own fixed destination, so none of them may carry a visitor
// notification.

type resendChannel struct {
	apiKey string
	from   string
}

func newResendChannel() Notifier {
	key := strings.TrimSpace(os.Getenv("NOTIFY_RESEND_API_KEY"))
	if key == "" {
		return nil
	}
	return &resendChannel{apiKey: key, from: notifyMailFrom()}
}

func (c *resendChannel) Name() string { return "resend" }
func (c *resendChannel) Kind() string { return KindMail }

// Endpoint is the sender address: the administrator's page shows it as the address a visitor's
// message comes from, which is the only address this channel owns.
func (c *resendChannel) Endpoint() string { return bareAddress(c.from) }

func (c *resendChannel) Deliver(ctx context.Context, msg Message) error {
	to := mailRecipient(msg)
	if to == "" {
		return fmt.Errorf("no recipient")
	}
	if msg.HTML == "" && msg.Body == "" {
		return fmt.Errorf("no body")
	}
	headers := map[string]string{}
	for _, h := range mailHeaders() {
		headers[h.name] = h.value
	}
	return postJSON(ctx, "https://api.resend.com/emails",
		map[string]string{"Authorization": "Bearer " + c.apiKey},
		resendPayload{
			From:    c.from,
			To:      []string{to},
			Subject: msg.Subject,
			Html:    msg.HTML,
			Text:    msg.Body,
			Headers: headers,
		})
}

// notifyMailFrom is the From header for outgoing mail. NOTIFY_MAIL_FROM overrides the
// site-derived default, for a deployment whose sending domain is not the site's.
func notifyMailFrom() string {
	if v := strings.TrimSpace(os.Getenv("NOTIFY_MAIL_FROM")); v != "" {
		return v
	}
	return notifyEmailFrom()
}

// envMailTo names the address administrator notifications are mailed to.
const envMailTo = "NOTIFY_MAIL_TO"

// notifyMailTo is the address an administrator notification goes to. It belongs to the mail
// layer rather than to the caller: a chat or webhook channel reaches its own fixed destination
// and needs no recipient, so only a mail channel can be missing one.
func notifyMailTo() string {
	return strings.TrimSpace(os.Getenv(envMailTo))
}

// mailRecipient is who a message goes to: the address its caller supplied, or the configured
// administrator address when it supplied none. A visitor notification always carries its own,
// because that address is one the visitor typed, so the fallback is the administrator's alone.
func mailRecipient(msg Message) string {
	if to := strings.TrimSpace(msg.To); to != "" {
		return to
	}
	return notifyMailTo()
}
