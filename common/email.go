package common

import (
	"bytes"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"html/template"
	"net/mail"
	"net/smtp"
	"slices"
	"strings"
	"time"
)

type EmailBody struct {
	html string
}

// NewEmailTextBody converts untrusted plain text to an HTML email body while
// preserving line breaks. HTML markup in content is always escaped.
func NewEmailTextBody(content string) EmailBody {
	escaped := template.HTMLEscapeString(content)
	escaped = strings.ReplaceAll(escaped, "\r\n", "\n")
	escaped = strings.ReplaceAll(escaped, "\r", "\n")
	escaped = strings.ReplaceAll(escaped, "\n", "<br>\n")
	return EmailBody{html: escaped}
}

// RenderEmailHTMLBody renders a trusted, server-defined HTML template. Dynamic
// values must be passed through data so html/template can escape each value for
// its text, attribute, or URL context.
func RenderEmailHTMLBody(templateSource string, data any) (EmailBody, error) {
	tmpl, err := template.New("email").Option("missingkey=error").Parse(templateSource)
	if err != nil {
		return EmailBody{}, fmt.Errorf("parse email template: %w", err)
	}

	var rendered bytes.Buffer
	if err := tmpl.Execute(&rendered, data); err != nil {
		return EmailBody{}, fmt.Errorf("render email template: %w", err)
	}
	return EmailBody{html: rendered.String()}, nil
}

func generateMessageID(sender string) (string, error) {
	address, err := mail.ParseAddress(sender)
	if err != nil {
		return "", fmt.Errorf("invalid SMTP account")
	}
	_, domain, found := strings.Cut(address.Address, "@")
	if !found || domain == "" {
		return "", fmt.Errorf("invalid SMTP account")
	}
	return fmt.Sprintf("<%d.%s@%s>", time.Now().UnixNano(), GetRandomString(12), domain), nil
}

func shouldUseSMTPLoginAuth() bool {
	if SMTPForceAuthLogin {
		return true
	}
	return isOutlookServer(SMTPAccount) || slices.Contains(EmailLoginAuthServerList, SMTPServer)
}

func getSMTPAuth() smtp.Auth {
	return AutoSMTPAuth(SMTPAccount, SMTPToken)
}

func shouldAuthenticateSMTP() bool {
	return SMTPAccount != "" && SMTPToken != ""
}

func smtpTLSConfig() *tls.Config {
	return &tls.Config{
		ServerName:         SMTPServer,
		InsecureSkipVerify: SMTPInsecureSkipVerify, // #nosec G402 -- admin-controlled SMTP compatibility option.
	}
}

func newSMTPClient(addr string) (*smtp.Client, error) {
	if SMTPSSLEnabled || (SMTPPort == 465 && !SMTPStartTLSEnabled) {
		conn, err := tls.Dial("tcp", addr, smtpTLSConfig())
		if err != nil {
			return nil, err
		}
		client, err := smtp.NewClient(conn, SMTPServer)
		if err != nil {
			_ = conn.Close()
			return nil, err
		}
		return client, nil
	}

	client, err := smtp.Dial(addr)
	if err != nil {
		return nil, err
	}

	if SMTPStartTLSEnabled {
		startTLSSupported, _ := client.Extension("STARTTLS")
		if !startTLSSupported {
			_ = client.Close()
			return nil, fmt.Errorf("SMTP server does not support STARTTLS")
		}
		if err := client.StartTLS(smtpTLSConfig()); err != nil {
			_ = client.Close()
			return nil, err
		}
	}

	return client, nil
}

func parseEmailRecipients(receiver string) ([]*mail.Address, error) {
	if receiver == "" || len(receiver) > 4096 {
		return nil, fmt.Errorf("invalid email recipient")
	}
	parts := strings.Split(receiver, ";")
	if len(parts) > 100 {
		return nil, fmt.Errorf("too many email recipients")
	}
	recipients := make([]*mail.Address, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			return nil, fmt.Errorf("invalid email recipient")
		}
		address, err := mail.ParseAddress(part)
		if err != nil || address.Address == "" {
			return nil, fmt.Errorf("invalid email recipient")
		}
		recipients = append(recipients, address)
	}
	if len(recipients) == 0 {
		return nil, fmt.Errorf("invalid email recipient")
	}
	return recipients, nil
}

func SendEmail(subject string, receiver string, body EmailBody) error {
	if SMTPServer == "" && SMTPAccount == "" {
		return fmt.Errorf("SMTP 服务器未配置")
	}

	sender := strings.TrimSpace(SMTPFrom)
	if sender == "" { // for compatibility
		sender = strings.TrimSpace(SMTPAccount)
	}
	fromAddress, err := mail.ParseAddress(sender)
	if err != nil || fromAddress.Address == "" {
		return fmt.Errorf("invalid SMTP account")
	}
	recipients, err := parseEmailRecipients(receiver)
	if err != nil {
		return err
	}
	id, err := generateMessageID(sender)
	if err != nil {
		return err
	}

	toHeaders := make([]string, 0, len(recipients))
	for _, recipient := range recipients {
		toHeaders = append(toHeaders, recipient.String())
	}
	fromHeader := (&mail.Address{Name: SystemName, Address: fromAddress.Address}).String()
	encodedSubject := fmt.Sprintf("=?UTF-8?B?%s?=", base64.StdEncoding.EncodeToString([]byte(subject)))
	message := []byte(fmt.Sprintf("To: %s\r\n"+
		"From: %s\r\n"+
		"Subject: %s\r\n"+
		"Date: %s\r\n"+
		"Message-ID: %s\r\n"+
		"MIME-Version: 1.0\r\n"+
		"Content-Transfer-Encoding: 8bit\r\n"+
		"Content-Type: text/html; charset=UTF-8\r\n\r\n%s\r\n",
		strings.Join(toHeaders, ", "), fromHeader, encodedSubject, time.Now().Format(time.RFC1123Z), id, body.html))
	auth := getSMTPAuth()
	addr := fmt.Sprintf("%s:%d", SMTPServer, SMTPPort)
	client, err := newSMTPClient(addr)
	if err != nil {
		return err
	}
	defer client.Close()
	if shouldAuthenticateSMTP() {
		if err = client.Auth(auth); err != nil {
			return err
		}
	}
	if err = client.Mail(fromAddress.Address); err != nil {
		return err
	}
	for _, recipient := range recipients {
		if err = client.Rcpt(recipient.Address); err != nil {
			return err
		}
	}
	w, err := client.Data()
	if err != nil {
		return err
	}
	// EmailBody can only contain context-escaped text or html/template output;
	// recipients and all header values are parsed or MIME-encoded above.
	_, err = w.Write(message) // lgtm[go/email-injection]
	if err != nil {
		return err
	}
	err = w.Close()
	if err != nil {
		return err
	}
	err = client.Quit()
	if err != nil {
		SysError(fmt.Sprintf("failed to complete SMTP session for %d recipient(s): %v", len(recipients), err))
	}
	return err
}
