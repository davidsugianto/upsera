package checker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"net"
	"strings"
	"time"

	"golang.org/x/net/dns/dnsmessage"

	"github.com/davidsugianto/upsera/internal/model"
)

// maxDNSAnswersInMessage caps how many resource records the result message
// lists.
const maxDNSAnswersInMessage = 3

// dnsQueryTypes maps a DNSConfig.RecordType to its wire query type, for the
// direct exchange used against a configured custom Resolver.
var dnsQueryTypes = map[string]dnsmessage.Type{
	"A":     dnsmessage.TypeA,
	"AAAA":  dnsmessage.TypeAAAA,
	"CNAME": dnsmessage.TypeCNAME,
	"MX":    dnsmessage.TypeMX,
	"TXT":   dnsmessage.TypeTXT,
	"NS":    dnsmessage.TypeNS,
}

func (c *Checker) checkDNS(ctx context.Context, m model.Monitor, timeoutS int) Result {
	var cfg DNSConfig
	if err := json.Unmarshal(m.Config, &cfg); err != nil {
		return Result{Status: model.StatusDown, Message: "invalid monitor config"}
	}

	start := time.Now()
	var answers []string
	var err error
	if cfg.Resolver != "" {
		// A configured Resolver must always be the thing actually
		// consulted: the system resolver would silently satisfy
		// well-known names (e.g. "localhost") from /etc/hosts, or apply
		// its own search list, before ever dialing it. Exchange directly
		// through the policy-guarded dial instead.
		answers, err = c.exchangeDNS(ctx, cfg.Resolver, cfg.RecordType, cfg.Hostname)
	} else {
		answers, err = lookupRecords(ctx, &net.Resolver{PreferGo: true}, cfg.RecordType, cfg.Hostname)
	}
	latency := time.Since(start)
	if err != nil {
		return Result{Status: model.StatusDown, Latency: latency, Message: describeErr(err, timeoutS)}
	}
	if len(answers) == 0 {
		return Result{Status: model.StatusDown, Latency: latency, Message: fmt.Sprintf("no %s records found", cfg.RecordType)}
	}

	if cfg.Expected != "" {
		expected := strings.ToLower(cfg.Expected)
		matched := false
		for _, a := range answers {
			if strings.Contains(strings.ToLower(a), expected) {
				matched = true
				break
			}
		}
		if !matched {
			return Result{Status: model.StatusDown, Latency: latency, Message: fmt.Sprintf("expected %q not found in answers", cfg.Expected)}
		}
	}

	return Result{Status: model.StatusUp, Latency: latency, Message: shortAnswerList(answers)}
}

// fqdn returns s with a trailing dot, matching the fully-qualified form the
// resolver (and our own direct queries) use for names on the wire.
func fqdn(s string) string {
	if strings.HasSuffix(s, ".") {
		return s
	}
	return s + "."
}

// lookupRecords resolves hostname for the given record type using resolver.
func lookupRecords(ctx context.Context, resolver *net.Resolver, recordType, hostname string) ([]string, error) {
	switch recordType {
	case "A", "AAAA":
		addrs, err := resolver.LookupIPAddr(ctx, hostname)
		if err != nil {
			return nil, err
		}
		wantV4 := recordType == "A"
		var answers []string
		for _, a := range addrs {
			if (a.IP.To4() != nil) == wantV4 {
				answers = append(answers, a.IP.String())
			}
		}
		return answers, nil
	case "CNAME":
		cname, err := resolver.LookupCNAME(ctx, hostname)
		if err != nil {
			return nil, err
		}
		if strings.EqualFold(cname, fqdn(hostname)) {
			// No CNAME record: LookupCNAME returns the (fully
			// qualified) hostname itself rather than an error when
			// there is none, per its documented behavior.
			return nil, nil
		}
		return []string{cname}, nil
	case "MX":
		mxs, err := resolver.LookupMX(ctx, hostname)
		if err != nil {
			return nil, err
		}
		answers := make([]string, len(mxs))
		for i, mx := range mxs {
			answers[i] = fmt.Sprintf("%s(%d)", mx.Host, mx.Pref)
		}
		return answers, nil
	case "TXT":
		return resolver.LookupTXT(ctx, hostname)
	case "NS":
		nss, err := resolver.LookupNS(ctx, hostname)
		if err != nil {
			return nil, err
		}
		answers := make([]string, len(nss))
		for i, ns := range nss {
			answers[i] = ns.Host
		}
		return answers, nil
	default:
		return nil, fmt.Errorf("unsupported record type %q", recordType)
	}
}

// exchangeDNS performs a single DNS query against server through the
// policy-guarded dial, bypassing the system resolver entirely (and so
// /etc/hosts and any search-list logic) so a monitor's configured custom
// Resolver is always what actually answers the check.
func (c *Checker) exchangeDNS(ctx context.Context, server, recordType, hostname string) ([]string, error) {
	qtype, ok := dnsQueryTypes[recordType]
	if !ok {
		return nil, fmt.Errorf("unsupported record type %q", recordType)
	}
	name, err := dnsmessage.NewName(fqdn(hostname))
	if err != nil {
		return nil, fmt.Errorf("invalid hostname %q", hostname)
	}
	query := dnsmessage.Message{
		Header: dnsmessage.Header{ID: uint16(rand.Uint32()), RecursionDesired: true},
		Questions: []dnsmessage.Question{{
			Name:  name,
			Type:  qtype,
			Class: dnsmessage.ClassINET,
		}},
	}
	packed, err := query.Pack()
	if err != nil {
		return nil, err
	}

	conn, err := c.policy.DialContext(ctx, "udp", server)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	if _, err := conn.Write(packed); err != nil {
		return nil, err
	}
	buf := make([]byte, 65535)
	n, err := conn.Read(buf)
	if err != nil {
		return nil, err
	}

	var resp dnsmessage.Message
	if err := resp.Unpack(buf[:n]); err != nil {
		return nil, fmt.Errorf("parsing dns response: %w", err)
	}
	if resp.Header.ID != query.Header.ID {
		return nil, errors.New("dns response id mismatch")
	}
	if resp.Header.RCode == dnsmessage.RCodeNameError {
		return nil, &net.DNSError{Err: "no such host", Name: hostname, IsNotFound: true}
	}
	if resp.Header.RCode != dnsmessage.RCodeSuccess {
		return nil, fmt.Errorf("dns server returned %s", resp.Header.RCode)
	}

	var answers []string
	for _, a := range resp.Answers {
		if a.Header.Type != qtype {
			continue
		}
		switch body := a.Body.(type) {
		case *dnsmessage.AResource:
			answers = append(answers, net.IP(body.A[:]).String())
		case *dnsmessage.AAAAResource:
			answers = append(answers, net.IP(body.AAAA[:]).String())
		case *dnsmessage.CNAMEResource:
			cname := body.CNAME.String()
			if !strings.EqualFold(cname, fqdn(hostname)) {
				answers = append(answers, cname)
			}
		case *dnsmessage.MXResource:
			answers = append(answers, fmt.Sprintf("%s(%d)", body.MX.String(), body.Pref))
		case *dnsmessage.TXTResource:
			answers = append(answers, strings.Join(body.TXT, ""))
		case *dnsmessage.NSResource:
			answers = append(answers, body.NS.String())
		}
	}
	return answers, nil
}

// shortAnswerList joins the first few answers into a short summary.
func shortAnswerList(answers []string) string {
	if len(answers) > maxDNSAnswersInMessage {
		answers = answers[:maxDNSAnswersInMessage]
	}
	return strings.Join(answers, ", ")
}
