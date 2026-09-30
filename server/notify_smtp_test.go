package main

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"mime"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A channel with no credentials is not a channel: naming it in NOTIFY_CHANNELS must not produce
// an entry that only fails when a message has to go out.
func TestSMTPChannelNeedsItsCredentials(t *testing.T) {
	t.Setenv(envSMTPAddr, "")
	t.Setenv(envSMTPUser, "noreply@example.com")
	t.Setenv(envSMTPPass, "secret")
	if newSMTPChannel() != nil {
		t.Error("an address-less SMTP configuration produced a channel")
	}

	t.Setenv(envSMTPAddr, "smtp.example.com:465")
	t.Setenv(envSMTPUser, "")
	if newSMTPChannel() != nil {
		t.Error("a user-less SMTP configuration produced a channel")
	}

	t.Setenv(envSMTPUser, "noreply@example.com")
	t.Setenv(envSMTPPass, "")
	if newSMTPChannel() != nil {
		t.Error("a password-less SMTP configuration produced a channel")
	}

	t.Setenv(envSMTPPass, "secret")
	ch := newSMTPChannel()
	if ch == nil {
		t.Fatal("a fully configured SMTP channel was not built")
	}
	if ch.Name() != "smtp" || ch.Kind() != KindMail {
		t.Errorf("channel is %q/%q, want smtp/%s", ch.Name(), ch.Kind(), KindMail)
	}
}

// The switch from one mail service to another is a change of NOTIFY_CHANNELS plus that
// channel's own variables; nothing else in the routing may have to change with it.
func TestSMTPChannelIsInTheRoutingAndTheKnownChannels(t *testing.T) {
	t.Setenv(envNotifyChannels, "smtp")
	t.Setenv(envSMTPAddr, "smtp.example.com:465")
	t.Setenv(envSMTPUser, "noreply@example.com")
	t.Setenv(envSMTPPass, "secret")

	set := loadNotifiers()
	if len(set.admin) != 1 || set.admin[0].Name() != "smtp" {
		t.Errorf("administrator channels = %v, want just smtp", notifierNames(set.admin))
	}
	// Nothing was said about visitors, so they go over the enabled channels that can reach an
	// address they were handed — which is what makes this a mail channel.
	if len(set.visitor) != 1 || set.visitor[0].Name() != "smtp" {
		t.Errorf("visitor channels = %v, want just smtp", notifierNames(set.visitor))
	}

	var smtp *channelStatus
	for _, st := range channelStatuses(resolveNotifiers()) {
		if st.Name == "smtp" {
			s := st
			smtp = &s
		}
	}
	if smtp == nil {
		t.Fatal("smtp is missing from the channels this build knows, so the page could not offer it")
	}
	if !smtp.Set || smtp.Kind != KindMail {
		t.Errorf("smtp: Set=%v Kind=%q, want a set mail channel",
			smtp.Set, smtp.Kind)
	}
}

// The sender's display name is the configured site name, so renaming the site in the panel
// renames the sender with it. The author's name is not it: that one names the person, and the
// From header names the site. The fixture's site_name is empty and its author is not, so a From
// built from the author cannot pass this.
func TestMailFromIsTheSiteName(t *testing.T) {
	home := useExampleSite(t)
	t.Setenv("BASE_URL", "https://example.com")
	t.Setenv("NOTIFY_MAIL_FROM", "")

	writeSiteIdentity(t, home, "Example Blog", "Someone Else")

	if got, want := notifyEmailFrom(), "Example Blog <noreply@example.com>"; got != want {
		t.Errorf("From = %q, want %q", got, want)
	}
	// Nothing overrides it, so both mail channels carry the same sender.
	if got, want := notifyMailFrom(), "Example Blog <noreply@example.com>"; got != want {
		t.Errorf("notifyMailFrom = %q, want %q", got, want)
	}

	// A site name that is not ASCII has to be encoded — raw UTF-8 in a header is what a
	// submission server refuses — while the envelope sender still comes out as the bare
	// address the server has verified.
	writeSiteIdentity(t, home, "示例站点", "Someone Else")
	ch := &smtpChannel{from: notifyEmailFrom()}
	if !strings.Contains(ch.from, "=?utf-8?") {
		t.Errorf("From = %q, want the display name encoded", ch.from)
	}
	if got, want := ch.envelopeFrom(), "noreply@example.com"; got != want {
		t.Errorf("envelope sender = %q, want %q", got, want)
	}
}

// writeSiteIdentity rewrites the fixture's site.json with the two names it carries.
func writeSiteIdentity(t *testing.T, home, siteName, author string) {
	t.Helper()
	path := filepath.Join(home, "config", "site.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	cfg["site_name"] = siteName
	cfg["author"] = author
	out, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal %s: %v", path, err)
	}
	if err := os.WriteFile(path, out, 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// Both forms of the message travel in one message, and the plain-text one comes first: a client
// that understands only that part still shows something.
func TestSMTPMessageCarriesBothParts(t *testing.T) {
	t.Setenv("BASE_URL", "https://example.com")
	c := &smtpChannel{from: "Example Site <noreply@example.com>"}
	raw := c.message(Message{To: "visitor@example.com", Subject: "收到留言", Body: "纯文本", HTML: "<p>html</p>"})

	if !strings.Contains(raw, "Content-Type: multipart/alternative; boundary=") {
		t.Errorf("the message is not a multipart/alternative:\n%s", raw)
	}
	plain, html := strings.Index(raw, "Content-Type: text/plain"), strings.Index(raw, "Content-Type: text/html")
	if plain < 0 || html < 0 || plain > html {
		t.Errorf("plain=%d html=%d, want the plain part present and before the html one", plain, html)
	}
	if got := base64.StdEncoding.EncodeToString([]byte("<p>html</p>")); !strings.Contains(raw, got) {
		t.Errorf("the html part is not the body that was handed over:\n%s", raw)
	}
	if got := base64.StdEncoding.EncodeToString([]byte("纯文本")); !strings.Contains(raw, got) {
		t.Errorf("the plain part is not the body that was handed over:\n%s", raw)
	}
	// A non-ASCII subject has to be encoded; a bare one is not a header a server will pass on.
	if got := mime.QEncoding.Encode("utf-8", "收到留言"); !strings.Contains(raw, "Subject: "+got) {
		t.Errorf("the subject was not encoded:\n%s", raw)
	}
	if !strings.Contains(raw, "From: Example Site <noreply@example.com>") {
		t.Errorf("the From header is not the configured one:\n%s", raw)
	}
	// Mail is CRLF-terminated throughout. A bare LF is accepted by some servers and silently
	// mangled by others.
	if strings.Contains(strings.ReplaceAll(raw, "\r\n", ""), "\n") {
		t.Errorf("the message carries a bare LF:\n%q", raw)
	}
}

// The session runs over implicit TLS: the connection is encrypted before the greeting, so there
// is no state in which the password or the message can travel in clear text. The server here is
// a plain listener, which is what a network that filters the submission port looks like.
func TestSMTPChannelNeverSpeaksPlaintext(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	first := make(chan byte, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		buf := make([]byte, 1)
		if _, err := conn.Read(buf); err == nil {
			first <- buf[0]
		}
	}()

	ch := &smtpChannel{addr: ln.Addr().String(), host: "127.0.0.1", user: "u", pass: "p", from: "u@example.com"}
	if err := ch.Deliver(context.Background(), Message{To: "visitor@example.com", Subject: "s", Body: "b"}); err == nil {
		t.Error("a server that does not speak TLS was reported as a successful delivery")
	}

	select {
	case b := <-first:
		if b != 0x16 {
			t.Errorf("the first byte on the wire was %#x, want a TLS handshake record", b)
		}
	case <-time.After(5 * time.Second):
		t.Error("nothing was written to the connection")
	}
}

// The whole session against a server that answers the way a submission server does: the
// envelope, the authentication and the body all have to arrive, and the envelope sender has to
// be the bare address of the From header — a submission server checks the envelope against the
// address it has verified, not against a display name.
func TestSMTPChannelDeliversASession(t *testing.T) {
	addr, cfg, seen := startSMTPTestServer(t)
	t.Setenv("BASE_URL", "https://example.com")
	t.Setenv(envSMTPAddr, addr)
	t.Setenv(envSMTPUser, "noreply@example.com")
	t.Setenv(envSMTPPass, "secret")
	t.Setenv("NOTIFY_MAIL_FROM", "Example Site <noreply@example.com>")

	ch := newSMTPChannel().(*smtpChannel)
	ch.tlsConfig = cfg

	msg := Message{To: "visitor@example.com", Subject: "收到留言", Body: "纯文本", HTML: "<p>html</p>"}
	if err := ch.Deliver(context.Background(), msg); err != nil {
		t.Fatalf("Deliver: %v", err)
	}

	var session smtpSession
	select {
	case session = <-seen:
	case <-time.After(5 * time.Second):
		t.Fatal("the server was never handed a message")
	}

	want := base64.StdEncoding.EncodeToString([]byte("\x00noreply@example.com\x00secret"))
	if session.auth != "PLAIN "+want {
		t.Errorf("authentication = %q, want PLAIN with the configured user and password", session.auth)
	}
	if session.mailFrom != "FROM:<noreply@example.com>" {
		t.Errorf("MAIL FROM = %q, want the bare address of the From header", session.mailFrom)
	}
	if len(session.rcptTo) != 1 || session.rcptTo[0] != "TO:<visitor@example.com>" {
		t.Errorf("RCPT TO = %v, want the recipient that was handed over", session.rcptTo)
	}
	if !strings.Contains(session.data, "Content-Type: multipart/alternative; boundary=") ||
		!strings.Contains(session.data, base64.StdEncoding.EncodeToString([]byte("<p>html</p>"))) {
		t.Errorf("the server received:\n%s", session.data)
	}
}

// smtpSession is what the test server was handed: the envelope, the authentication line and the
// message body.
type smtpSession struct {
	auth     string
	mailFrom string
	rcptTo   []string
	data     string
}

// startSMTPTestServer answers one SMTP session over implicit TLS, the way the channel's port
// does, and reports what it was handed. The certificate is self-signed and the channel is given
// the pool holding it: nothing signs a test server's certificate.
func startSMTPTestServer(t *testing.T) (string, *tls.Config, chan smtpSession) {
	t.Helper()
	cert, pool := selfSignedCert(t)
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{cert}})
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	seen := make(chan smtpSession, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		if session, err := serveSMTP(conn); err == nil {
			seen <- session
		}
	}()
	return ln.Addr().String(), &tls.Config{RootCAs: pool, ServerName: "127.0.0.1"}, seen
}

func serveSMTP(conn net.Conn) (smtpSession, error) {
	var session smtpSession
	r := bufio.NewReader(conn)
	w := bufio.NewWriter(conn)
	say := func(line string) error {
		if _, err := w.WriteString(line + "\r\n"); err != nil {
			return err
		}
		return w.Flush()
	}
	if err := say("220 test ESMTP"); err != nil {
		return session, err
	}
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return session, err
		}
		verb, arg, _ := strings.Cut(strings.TrimRight(line, "\r\n"), " ")
		switch strings.ToUpper(verb) {
		case "EHLO", "HELO":
			if err := say("250-test"); err != nil {
				return session, err
			}
			if err := say("250 AUTH PLAIN"); err != nil {
				return session, err
			}
		case "AUTH":
			session.auth = arg
			if err := say("235 ok"); err != nil {
				return session, err
			}
		case "MAIL":
			session.mailFrom = arg
			if err := say("250 ok"); err != nil {
				return session, err
			}
		case "RCPT":
			session.rcptTo = append(session.rcptTo, arg)
			if err := say("250 ok"); err != nil {
				return session, err
			}
		case "DATA":
			if err := say("354 go ahead"); err != nil {
				return session, err
			}
			var body strings.Builder
			for {
				l, err := r.ReadString('\n')
				if err != nil {
					return session, err
				}
				if l == ".\r\n" {
					break
				}
				body.WriteString(l)
			}
			session.data = body.String()
			if err := say("250 queued"); err != nil {
				return session, err
			}
		case "QUIT":
			_ = say("221 bye")
			return session, nil
		default:
			if err := say("250 ok"); err != nil {
				return session, err
			}
		}
	}
}

// selfSignedCert is a certificate for 127.0.0.1 that is its own root, plus the pool a client
// verifies it with.
func selfSignedCert(t *testing.T) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{Organization: []string{"Silphuu test"}},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("certificate: %v", err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(leaf)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}, pool
}
