package testutil

import (
	"bufio"
	"net"
	"strings"
	"sync"
	"testing"
)

// SMTPMessage is one message accepted by FakeSMTP.
type SMTPMessage struct {
	From string
	To   []string
	Data string
}

// FakeSMTP starts a minimal plain-text SMTP listener on 127.0.0.1:0,
// understanding just enough of RFC 5321 for net/smtp's client: greeting,
// EHLO/HELO, MAIL FROM, RCPT TO, DATA, RSET, NOOP and QUIT. It never
// advertises STARTTLS or AUTH, so it only serves tls:"none" configs. The
// listener is closed via t.Cleanup.
func FakeSMTP(t testing.TB) (addr string, messages func() []SMTPMessage) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("fake smtp: listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	var mu sync.Mutex
	var received []SMTPMessage

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go serveSMTPConn(conn, &mu, &received)
		}
	}()

	return ln.Addr().String(), func() []SMTPMessage {
		mu.Lock()
		defer mu.Unlock()
		out := make([]SMTPMessage, len(received))
		copy(out, received)
		return out
	}
}

func serveSMTPConn(conn net.Conn, mu *sync.Mutex, received *[]SMTPMessage) {
	defer conn.Close()
	r := bufio.NewReader(conn)
	writeLine := func(s string) { _, _ = conn.Write([]byte(s + "\r\n")) }

	writeLine("220 upsera-fake-smtp ESMTP")
	var cur SMTPMessage
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		upper := strings.ToUpper(line)
		switch {
		case strings.HasPrefix(upper, "EHLO"), strings.HasPrefix(upper, "HELO"):
			writeLine("250 upsera-fake-smtp")
		case strings.HasPrefix(upper, "MAIL FROM:"):
			cur = SMTPMessage{From: smtpExtractAddr(line)}
			writeLine("250 ok")
		case strings.HasPrefix(upper, "RCPT TO:"):
			cur.To = append(cur.To, smtpExtractAddr(line))
			writeLine("250 ok")
		case strings.HasPrefix(upper, "DATA"):
			writeLine("354 go ahead")
			var data strings.Builder
			for {
				dl, err := r.ReadString('\n')
				if err != nil {
					return
				}
				if dl == ".\r\n" || dl == ".\n" {
					break
				}
				data.WriteString(dl)
			}
			cur.Data = data.String()
			mu.Lock()
			*received = append(*received, cur)
			mu.Unlock()
			cur = SMTPMessage{}
			writeLine("250 ok")
		case upper == "RSET":
			cur = SMTPMessage{}
			writeLine("250 ok")
		case upper == "NOOP":
			writeLine("250 ok")
		case upper == "QUIT":
			writeLine("221 bye")
			return
		default:
			writeLine("250 ok")
		}
	}
}

// smtpExtractAddr pulls the address out of "MAIL FROM:<a>" / "RCPT TO:<b>",
// tolerating trailing ESMTP parameters after the closing '>'.
func smtpExtractAddr(line string) string {
	i := strings.Index(line, "<")
	j := strings.Index(line, ">")
	if i >= 0 && j > i {
		return line[i+1 : j]
	}
	return strings.TrimSpace(line)
}
