package checker

import (
	"net"
	"strconv"
	"strings"
	"testing"

	"github.com/davidsugianto/upsera/internal/model"
)

func TestTCPOpenPort(t *testing.T) {
	t.Parallel()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			conn.Close()
		}
	}()

	host, portStr, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatal(err)
	}

	c := newTestChecker()
	m := mustMonitor(t, model.TypeTCP, TCPConfig{Host: host, Port: port}, 5)
	res := c.Check(t.Context(), m)
	if res.Status != model.StatusUp {
		t.Fatalf("Status = %v, Message = %q, want Up", res.Status, res.Message)
	}
	if res.Latency <= 0 {
		t.Error("Latency not measured")
	}
}

func TestTCPClosedPort(t *testing.T) {
	t.Parallel()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	host, portStr, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	ln.Close() // nothing listens here now

	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatal(err)
	}

	c := newTestChecker()
	m := mustMonitor(t, model.TypeTCP, TCPConfig{Host: host, Port: port}, 5)
	res := c.Check(t.Context(), m)
	if res.Status != model.StatusDown || res.Message != "connection refused" {
		t.Fatalf("got status=%v message=%q, want Down 'connection refused'", res.Status, res.Message)
	}
}

func TestTCPBlockedTarget(t *testing.T) {
	t.Parallel()
	c := newTestChecker()
	m := mustMonitor(t, model.TypeTCP, TCPConfig{Host: "169.254.169.254", Port: 80}, 5)
	res := c.Check(t.Context(), m)
	if res.Status != model.StatusDown || !strings.Contains(res.Message, "blocked") {
		t.Fatalf("got status=%v message=%q, want Down mentioning blocked", res.Status, res.Message)
	}
}
