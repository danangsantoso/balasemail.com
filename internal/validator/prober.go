package validator

import (
	"context"
	"errors"
	"net"
	"net/smtp"
	"net/textproto"
	"time"
)

// SMTPProber talks to the recipient MX on port 25 and stops before DATA,
// so no message is ever delivered.
type SMTPProber struct {
	HeloName string
	MailFrom string
	Timeout  time.Duration
	Port     string // default "25"
}

func (p *SMTPProber) Probe(ctx context.Context, mxHost string, rcpts []string) ([]int, error) {
	port := p.Port
	if port == "" {
		port = "25"
	}
	timeout := p.Timeout
	if timeout == 0 {
		timeout = 10 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort(mxHost, port))
	if err != nil {
		return nil, err
	}
	deadline, _ := ctx.Deadline()
	_ = conn.SetDeadline(deadline)
	c, err := smtp.NewClient(conn, mxHost)
	if err != nil {
		conn.Close()
		return nil, err
	}
	defer c.Close()
	if err := c.Hello(p.HeloName); err != nil {
		return nil, err
	}
	if err := c.Mail(p.MailFrom); err != nil {
		return nil, err
	}
	codes := make([]int, len(rcpts))
	for i, r := range rcpts {
		codes[i] = replyCode(c.Rcpt(r))
	}
	_ = c.Quit()
	return codes, nil
}

func replyCode(err error) int {
	if err == nil {
		return 250
	}
	var tp *textproto.Error
	if errors.As(err, &tp) {
		return tp.Code
	}
	return 0
}
