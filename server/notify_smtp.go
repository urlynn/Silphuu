package main

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"mime"
	"net"
	"net/mail"
	"net/smtp"
	"os"
	"strconv"
	"strings"
	"time"
)

// The mail channel over a submission server's implicit-TLS port.
//
// Implicit TLS rather than submission with STARTTLS: a STARTTLS session opens in clear text and
// upgrades only when the server advertises the capability, so a network that strips that line —
// which is what a filtered port looks like — leaves the client either failing or handing the
// password and the message over unencrypted. There is no such state here: the first byte on the
// wire is the handshake.
//
// net/smtp does not dial TLS itself, so the connection comes from tls.Dialer and the session is
// started on the connection it returns. That is also what makes PLAIN authentication work:
// net/smtp refuses to send a password over a connection that is not TLS, and it decides that by
// the type of the connection it was handed, not by any option.
//
// The port carries no vendor: the whole "host:port" comes from the environment, and the only
// name in this file is the one the operator set.

const (
	envSMTPAddr = "NOTIFY_SMTP_ADDR"
	envSMTPUser = "NOTIFY_SMTP_USER"
	envSMTPPass = "NOTIFY_SMTP_PASS"
)

type smtpChannel struct {
	addr string
	// host is the name the session identifies itself to and the name the server's certificate
	// has to be issued for.
	host string
	user string
	pass string
	from string
	// tlsConfig replaces the dial configuration. Nil means the production one: verify the
	// server against the system roots, naming host.
	tlsConfig *tls.Config
}

func newSMTPChannel() Notifier {
	addr := strings.TrimSpace(os.Getenv(envSMTPAddr))
	user := strings.TrimSpace(os.Getenv(envSMTPUser))
	pass := os.Getenv(envSMTPPass)
	if addr == "" || user == "" || pass == "" {
		return nil
	}
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	return &smtpChannel{addr: addr, host: host, user: user, pass: pass, from: notifyMailFrom()}
}

func (c *smtpChannel) Name() string { return "smtp" }
func (c *smtpChannel) Kind() string { return KindMail }

// Endpoint is the sender address: the administrator's page shows it as the address a visitor's
// message comes from, which is the only address this channel owns.
func (c *smtpChannel) Endpoint() string { return c.envelopeFrom() }

func (c *smtpChannel) Deliver(ctx context.Context, msg Message) error {
	to := mailRecipient(msg)
	if to == "" {
		return fmt.Errorf("no recipient")
	}
	if msg.HTML == "" && msg.Body == "" {
		return fmt.Errorf("no body")
	}
	parsed, err := mail.ParseAddress(to)
	if err != nil {
		return fmt.Errorf("recipient %q: %w", to, err)
	}
	// The header and the envelope name the same recipient, so the message carries the address
	// this delivery resolved rather than the one the caller left empty.
	msg.To = to
	return c.send(ctx, parsed.Address, c.message(msg))
}

// send runs one SMTP session: the handshake tls.Dialer already completed, then authentication,
// envelope and body.
func (c *smtpChannel) send(ctx context.Context, rcpt, raw string) error {
	dialer := &tls.Dialer{
		NetDialer: &net.Dialer{Timeout: notifyHTTPTimeout},
		Config:    c.dialConfig(),
	}
	conn, err := dialer.DialContext(ctx, "tcp", c.addr)
	if err != nil {
		return err
	}
	defer conn.Close()

	// The session past the handshake is a sequence of reads and writes that take no context of
	// their own. The deadline is what bounds it; closing the connection on cancellation is what
	// keeps a server that has gone quiet from holding the caller for the whole of it.
	_ = conn.SetDeadline(time.Now().Add(notifyHTTPTimeout))
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()

	client, err := smtp.NewClient(conn, c.host)
	if err != nil {
		return err
	}
	if err := client.Auth(smtp.PlainAuth("", c.user, c.pass, c.host)); err != nil {
		return err
	}
	if err := client.Mail(c.envelopeFrom()); err != nil {
		return err
	}
	if err := client.Rcpt(rcpt); err != nil {
		return err
	}
	w, err := client.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write([]byte(raw)); err != nil {
		_ = w.Close()
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	// Past a successful DATA the message belongs to the server. QUIT only closes the session,
	// and reporting its failure would report a delivery that happened as one that did not.
	_ = client.Quit()
	return nil
}

func (c *smtpChannel) dialConfig() *tls.Config {
	if c.tlsConfig != nil {
		return c.tlsConfig
	}
	return &tls.Config{ServerName: c.host, MinVersion: tls.VersionTLS12}
}

// envelopeFrom is the MAIL FROM address: the bare address out of the From header, because the
// submission server checks the envelope against the address it has verified, and the display
// name is not part of it. Taking it from the header rather than from a second variable is what
// keeps the envelope and the header from ever naming different senders.
func (c *smtpChannel) envelopeFrom() string {
	return bareAddress(c.from)
}

// message renders the whole message: the headers, then one base64 body part per form of the
// message. Plain text comes first: RFC 2046 orders the alternatives by increasing faithfulness,
// so a client that understands only the first one still shows something.
func (c *smtpChannel) message(msg Message) string {
	boundary := "=_silphuu_" + strconv.FormatInt(time.Now().UnixNano(), 36)
	var b strings.Builder
	line := func(format string, args ...any) { fmt.Fprintf(&b, format+"\r\n", args...) }

	line("From: %s", c.from)
	line("To: %s", msg.To)
	line("Subject: %s", mime.QEncoding.Encode("utf-8", msg.Subject))
	line("Date: %s", time.Now().Format(time.RFC1123Z))
	line("MIME-Version: 1.0")
	for _, h := range mailHeaders() {
		line("%s: %s", h.name, h.value)
	}
	line("Content-Type: multipart/alternative; boundary=%q", boundary)
	line("")
	line("--%s", boundary)
	line("Content-Type: text/plain; charset=utf-8")
	line("Content-Transfer-Encoding: base64")
	line("")
	b.WriteString(wrapBase64(msg.Body))
	line("")
	line("--%s", boundary)
	line("Content-Type: text/html; charset=utf-8")
	line("Content-Transfer-Encoding: base64")
	line("")
	b.WriteString(wrapBase64(msg.HTML))
	line("")
	line("--%s--", boundary)
	return b.String()
}

// wrapBase64 encodes data as base64 in 76-character lines: a mail body may not carry a line
// longer than 998 octets, and a rendered page is one long line.
func wrapBase64(data string) string {
	enc := base64.StdEncoding.EncodeToString([]byte(data))
	var b strings.Builder
	for len(enc) > 76 {
		b.WriteString(enc[:76])
		b.WriteString("\r\n")
		enc = enc[76:]
	}
	b.WriteString(enc)
	return b.String()
}
