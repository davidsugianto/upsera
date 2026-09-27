package checker

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"strings"
	"syscall"

	"github.com/davidsugianto/upsera/internal/netpolicy"
)

// describeErr turns a network error into a short, safe-to-store reason. It
// never includes response bodies or full URLs.
func describeErr(err error, timeoutS int) string {
	if msg, ok := tlsMessage(err); ok {
		return msg
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Sprintf("timeout after %ds", timeoutS)
	}
	if errors.Is(err, netpolicy.ErrBlocked) {
		cause := strings.TrimPrefix(rootCause(err, netpolicy.ErrBlocked).Error(), netpolicy.ErrBlocked.Error()+": ")
		return "blocked by outbound policy: " + cause
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		if dnsErr.IsNotFound {
			return "dns lookup failed: no such host"
		}
		if dnsErr.IsTimeout {
			return "dns lookup timed out"
		}
		return "dns lookup failed"
	}
	if errors.Is(err, syscall.ECONNREFUSED) {
		return "connection refused"
	}
	if errors.Is(err, syscall.ECONNRESET) {
		return "connection reset"
	}
	if errors.Is(err, syscall.EHOSTUNREACH) {
		return "host unreachable"
	}
	if errors.Is(err, syscall.ENETUNREACH) {
		return "network unreachable"
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return "connection closed by server"
	}
	// *url.Error's Error() prepends the full request URL (which may carry
	// secrets in its query string); describe the wrapped cause instead so
	// the fallback below never echoes it back.
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return describeErr(urlErr.Err, timeoutS)
	}
	return err.Error()
}

// rootCause unwraps err down to the innermost error that still wraps
// target directly, i.e. the one whose own Unwrap returns target. That
// preserves any %w-wrapped detail text (such as the blocked-policy reason)
// instead of continuing past it to the bare sentinel.
func rootCause(err, target error) error {
	for {
		next := errors.Unwrap(err)
		if next == nil || next == target {
			return err
		}
		err = next
	}
}

// tlsMessage reports a short reason for a TLS certificate verification
// failure, if err is one.
func tlsMessage(err error) (string, bool) {
	var certErr x509.CertificateInvalidError
	if errors.As(err, &certErr) {
		if certErr.Reason == x509.Expired {
			return "TLS certificate expired", true
		}
		return "TLS certificate invalid", true
	}
	var unknownAuth x509.UnknownAuthorityError
	if errors.As(err, &unknownAuth) {
		return "TLS certificate signed by unknown authority", true
	}
	var hostErr x509.HostnameError
	if errors.As(err, &hostErr) {
		return "TLS hostname mismatch", true
	}
	var verifyErr *tls.CertificateVerificationError
	if errors.As(err, &verifyErr) {
		return "TLS certificate verification failed", true
	}
	return "", false
}
