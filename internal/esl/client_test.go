package esl

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"net/textproto"
	"strings"
	"testing"
	"time"
)

// fakeESL is a tiny mod_event_socket stand-in: greets, checks the password,
// answers `api <cmd>` from a table, and records every command it saw.
type fakeESL struct {
	addr     string
	password string
	replies  map[string]string
	seen     chan string
}

func startFakeESL(t *testing.T, password string, replies map[string]string) *fakeESL {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	t.Cleanup(func() { _ = ln.Close() })

	f := &fakeESL{addr: ln.Addr().String(), password: password, replies: replies, seen: make(chan string, 16)}

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}

			go f.serve(conn)
		}
	}()

	return f
}

func (f *fakeESL) serve(conn net.Conn) {
	defer func() { _ = conn.Close() }()

	r := textproto.NewReader(bufio.NewReader(conn))

	_, _ = fmt.Fprint(conn, "Content-Type: auth/request\n\n")

	for {
		line, err := r.ReadLine()
		if err != nil {
			return
		}

		if line == "" {
			continue
		}

		switch {
		case strings.HasPrefix(line, "auth "):
			if strings.TrimPrefix(line, "auth ") != f.password {
				_, _ = fmt.Fprint(conn, "Content-Type: command/reply\nReply-Text: -ERR invalid\n\n")

				return
			}

			_, _ = fmt.Fprint(conn, "Content-Type: command/reply\nReply-Text: +OK accepted\n\n")
		case strings.HasPrefix(line, "api "):
			cmd := strings.TrimPrefix(line, "api ")
			f.seen <- cmd

			body, ok := f.replies[cmd]
			if !ok {
				body = "-ERR " + cmd + " Command not found!\n"
			}

			// A stray log frame first: the client must skip it.
			_, _ = fmt.Fprint(conn, "Content-Type: log/data\nContent-Length: 5\n\nnoise")
			_, _ = fmt.Fprintf(conn, "Content-Type: api/response\nContent-Length: %d\n\n%s", len(body), body)
		case line == "exit":
			return
		}
	}
}

func TestAPI_RunsCommandsOnOneConnection(t *testing.T) {
	f := startFakeESL(t, "secret", map[string]string{
		"status":           "UP 0 years, 0 days\n",
		"show calls count": "\n3 total.\n",
	})

	c := Client{Addr: f.addr, Password: "secret", Timeout: time.Second}

	got, err := c.API(context.Background(), "status", "show calls count")
	if err != nil {
		t.Fatalf("API: %v", err)
	}

	if len(got) != 2 || !strings.HasPrefix(got[0], "UP") || !strings.Contains(got[1], "3 total") {
		t.Errorf("replies = %q", got)
	}
}

func TestAPI_WrongPasswordIsAuthError(t *testing.T) {
	f := startFakeESL(t, "secret", nil)

	c := Client{Addr: f.addr, Password: "nope", Timeout: time.Second}

	_, err := c.API(context.Background(), "status")
	if !errors.Is(err, ErrAuth) {
		t.Fatalf("err = %v, want ErrAuth", err)
	}
}

// A newline inside a command would be framed by FreeSWITCH as a SECOND
// command — the client must refuse it before dialing.
func TestAPI_RefusesMultilineCommands(t *testing.T) {
	f := startFakeESL(t, "secret", nil)

	c := Client{Addr: f.addr, Password: "secret", Timeout: time.Second}

	for _, cmd := range []string{"status\n\napi hupall", "status\rhupall", "  "} {
		if _, err := c.API(context.Background(), cmd); err == nil {
			t.Errorf("command %q was accepted", cmd)
		}
	}

	select {
	case cmd := <-f.seen:
		t.Errorf("fake ESL received %q; a refused command must never be sent", cmd)
	default:
	}
}

func TestAPI_DialErrorNamesTheAddress(t *testing.T) {
	c := Client{Addr: "127.0.0.1:1", Password: "x", Timeout: 200 * time.Millisecond}

	_, err := c.API(context.Background(), "status")
	if err == nil || !strings.Contains(err.Error(), "127.0.0.1:1") {
		t.Fatalf("err = %v, want a dial error naming the address", err)
	}
}
