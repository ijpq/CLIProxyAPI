package billing

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/smtp"
	"os"
	"strconv"
	"strings"
	"time"
)

// EmailSender delivers transactional emails (e.g. login codes). A nil sender
// means email delivery is not configured and email features are disabled.
type EmailSender interface {
	Send(ctx context.Context, to, subject, htmlBody string) error
}

// SMTPSender is a minimal SMTP client configured from environment variables.
type SMTPSender struct {
	host     string
	port     int
	username string
	password string
	from     string
}

// NewEmailSenderFromEnv builds an SMTPSender from BILLING_SMTP_* variables.
// Returns nil when BILLING_SMTP_HOST is empty (email features disabled).
//
//	BILLING_SMTP_HOST      smtp host (e.g. smtp.gmail.com)     [required]
//	BILLING_SMTP_PORT      smtp port (default 587)
//	BILLING_SMTP_USERNAME  auth username (usually the address)
//	BILLING_SMTP_PASSWORD  auth password / app password
//	BILLING_SMTP_FROM      From address (default = username)
func NewEmailSenderFromEnv() *SMTPSender {
	host := strings.TrimSpace(os.Getenv("BILLING_SMTP_HOST"))
	if host == "" {
		return nil
	}
	port := 587
	if v := strings.TrimSpace(os.Getenv("BILLING_SMTP_PORT")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			port = n
		}
	}
	username := strings.TrimSpace(os.Getenv("BILLING_SMTP_USERNAME"))
	from := strings.TrimSpace(os.Getenv("BILLING_SMTP_FROM"))
	if from == "" {
		from = username
	}
	return &SMTPSender{
		host:     host,
		port:     port,
		username: username,
		password: os.Getenv("BILLING_SMTP_PASSWORD"),
		from:     from,
	}
}

// Send delivers an HTML email. Port 465 uses implicit TLS; other ports use
// STARTTLS. Auth is applied when a username is configured.
func (s *SMTPSender) Send(ctx context.Context, to, subject, htmlBody string) error {
	if s == nil {
		return fmt.Errorf("billing: email sender not configured")
	}
	addr := net.JoinHostPort(s.host, strconv.Itoa(s.port))
	msg := s.buildMessage(to, subject, htmlBody)

	deadline := time.Now().Add(20 * time.Second)
	if dl, ok := ctx.Deadline(); ok && dl.Before(deadline) {
		deadline = dl
	}
	dialer := &net.Dialer{Deadline: deadline}

	var conn net.Conn
	var err error
	if s.port == 465 {
		conn, err = tls.DialWithDialer(dialer, "tcp", addr, &tls.Config{ServerName: s.host})
	} else {
		conn, err = dialer.DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return fmt.Errorf("billing: smtp dial: %w", err)
	}

	client, err := smtp.NewClient(conn, s.host)
	if err != nil {
		_ = conn.Close()
		return fmt.Errorf("billing: smtp client: %w", err)
	}
	defer func() { _ = client.Close() }()

	if s.port != 465 {
		if ok, _ := client.Extension("STARTTLS"); ok {
			if err = client.StartTLS(&tls.Config{ServerName: s.host}); err != nil {
				return fmt.Errorf("billing: smtp starttls: %w", err)
			}
		}
	}
	if s.username != "" {
		if err = client.Auth(smtp.PlainAuth("", s.username, s.password, s.host)); err != nil {
			return fmt.Errorf("billing: smtp auth: %w", err)
		}
	}
	if err = client.Mail(s.from); err != nil {
		return fmt.Errorf("billing: smtp mail from: %w", err)
	}
	if err = client.Rcpt(to); err != nil {
		return fmt.Errorf("billing: smtp rcpt: %w", err)
	}
	w, err := client.Data()
	if err != nil {
		return fmt.Errorf("billing: smtp data: %w", err)
	}
	if _, err = w.Write(msg); err != nil {
		return fmt.Errorf("billing: smtp write: %w", err)
	}
	if err = w.Close(); err != nil {
		return fmt.Errorf("billing: smtp close body: %w", err)
	}
	return client.Quit()
}

func (s *SMTPSender) buildMessage(to, subject, htmlBody string) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "From: %s\r\n", s.from)
	fmt.Fprintf(&b, "To: %s\r\n", to)
	fmt.Fprintf(&b, "Subject: %s\r\n", subject)
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/html; charset=UTF-8\r\n")
	b.WriteString("\r\n")
	b.WriteString(htmlBody)
	return []byte(b.String())
}
