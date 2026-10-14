// Package esl is a minimal FreeSWITCH Event Socket (inbound) client: dial,
// authenticate, run `api <command>`, read the reply. Standard library only.
//
// One connection per call, on purpose. The plugin answers a dashboard that
// polls every ~15s and an operator who clicks an action now and then; a
// persistent socket would buy nothing at that rate and would add reconnect,
// event-filter and concurrency handling. A fresh socket per call also means a
// FreeSWITCH restart is invisible to us — the next call just dials again.
//
// Protocol (inbound mode, mod_event_socket):
//
//	server → "Content-Type: auth/request\n\n"
//	client → "auth <password>\n\n"
//	server → "Content-Type: command/reply\nReply-Text: +OK accepted\n\n"
//	client → "api <command>\n\n"
//	server → "Content-Type: api/response\nContent-Length: N\n\n<N bytes>"
package esl

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/textproto"
	"strconv"
	"strings"
	"time"
)

// ErrAuth is returned when FreeSWITCH rejects the password (or the ACL
// refuses the connection after the greeting).
var ErrAuth = errors.New("esl: authentication failed")

// Client runs api commands against one FreeSWITCH event socket.
type Client struct {
	Addr     string
	Password string
	Timeout  time.Duration
}

// API runs one or more api commands on a single connection and returns each
// reply body in order. Several commands share a socket so one dashboard poll
// (status + counts) costs one TCP handshake and one auth, not three.
func (c Client) API(ctx context.Context, commands ...string) ([]string, error) {
	for _, cmd := range commands {
		if err := validCommand(cmd); err != nil {
			return nil, err
		}
	}

	conn, err := c.dial(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = conn.Close() }()

	r := textproto.NewReader(bufio.NewReader(conn))

	if err := authenticate(conn, r, c.Password); err != nil {
		return nil, err
	}

	replies := make([]string, 0, len(commands))

	for _, cmd := range commands {
		body, err := runAPI(conn, r, cmd)
		if err != nil {
			return nil, err
		}

		replies = append(replies, body)
	}

	_, _ = io.WriteString(conn, "exit\n\n")

	return replies, nil
}

// validCommand refuses anything that could smuggle a second ESL command: the
// protocol frames commands by newline, so a "\n" inside an argument would be
// read by FreeSWITCH as a new command (e.g. "status\n\napi hupall").
func validCommand(cmd string) error {
	if strings.TrimSpace(cmd) == "" {
		return errors.New("esl: empty command")
	}

	if strings.ContainsAny(cmd, "\r\n") {
		return errors.New("esl: command must be a single line")
	}

	return nil
}

func (c Client) dial(ctx context.Context) (net.Conn, error) {
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}

	d := net.Dialer{Timeout: timeout}

	conn, err := d.DialContext(ctx, "tcp", c.Addr)
	if err != nil {
		return nil, fmt.Errorf("esl: dial %s: %w", c.Addr, err)
	}

	deadline := time.Now().Add(timeout)
	if dl, ok := ctx.Deadline(); ok && dl.Before(deadline) {
		deadline = dl
	}

	_ = conn.SetDeadline(deadline)

	return conn, nil
}

func authenticate(conn net.Conn, r *textproto.Reader, password string) error {
	greeting, err := r.ReadMIMEHeader()
	if err != nil {
		return fmt.Errorf("esl: read greeting: %w", err)
	}

	// A connection the ACL refuses gets a disconnect notice instead of the
	// auth request — report it as an auth failure, which is what fixing it
	// looks like for the operator (ACL or password).
	if greeting.Get("Content-Type") != "auth/request" {
		return fmt.Errorf("%w: expected auth/request, got %q", ErrAuth, greeting.Get("Content-Type"))
	}

	if _, err := fmt.Fprintf(conn, "auth %s\n\n", password); err != nil {
		return fmt.Errorf("esl: send auth: %w", err)
	}

	reply, err := r.ReadMIMEHeader()
	if err != nil {
		return fmt.Errorf("esl: read auth reply: %w", err)
	}

	if !strings.HasPrefix(reply.Get("Reply-Text"), "+OK") {
		return fmt.Errorf("%w: %s", ErrAuth, reply.Get("Reply-Text"))
	}

	return nil
}

func runAPI(conn net.Conn, r *textproto.Reader, cmd string) (string, error) {
	if _, err := fmt.Fprintf(conn, "api %s\n\n", cmd); err != nil {
		return "", fmt.Errorf("esl: send api %q: %w", cmd, err)
	}

	for {
		header, err := r.ReadMIMEHeader()
		if err != nil {
			return "", fmt.Errorf("esl: read reply to %q: %w", cmd, err)
		}

		length, _ := strconv.Atoi(header.Get("Content-Length"))

		body := make([]byte, length)
		if _, err := io.ReadFull(r.R, body); err != nil {
			return "", fmt.Errorf("esl: read body of %q: %w", cmd, err)
		}

		// Inbound sockets can carry other frames (a log line, a
		// disconnect notice); only the api/response answers us.
		if header.Get("Content-Type") == "api/response" {
			return string(body), nil
		}
	}
}
