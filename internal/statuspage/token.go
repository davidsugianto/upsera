package statuspage

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"time"

	"github.com/davidsugianto/upsera/internal/model"
)

// confirmTTL is how long a confirmation link stays valid.
const confirmTTL = 48 * time.Hour

// maxTokenLen bounds what verify will even decode (a confirm token is 66
// characters), so junk in the query string costs nothing.
const maxTokenLen = 128

// Token purposes. The purpose is part of both the payload and the MAC
// input, so a token minted for one purpose never verifies for the other.
const (
	tokenConfirm     byte = 'c'
	tokenUnsubscribe byte = 'u'
)

var (
	errTokenInvalid = errors.New("statuspage: invalid token")
	errTokenExpired = errors.New("statuspage: expired token")
)

var tokenEnc = base64.RawURLEncoding

// tokenMAC is HMAC-SHA256 over a fixed domain-separation prefix and the
// payload, keyed with the instance secret.
func (s *Service) tokenMAC(payload []byte) []byte {
	m := hmac.New(sha256.New, s.opts.Secret)
	m.Write([]byte("upsera/statuspage/link/v1\x00"))
	m.Write(payload)
	return m.Sum(nil)
}

// signToken returns base64url(payload || mac). It returns "" when no secret
// is configured, so links can never be forged with an empty key.
func (s *Service) signToken(payload []byte) string {
	if len(s.opts.Secret) == 0 {
		return ""
	}
	return tokenEnc.EncodeToString(append(payload, s.tokenMAC(payload)...))
}

// openToken checks tok's MAC and returns its payload, which starts with the
// purpose byte. The comparison is constant time.
func (s *Service) openToken(tok string, purpose byte, payloadLen int) ([]byte, error) {
	if len(s.opts.Secret) == 0 || tok == "" || len(tok) > maxTokenLen {
		return nil, errTokenInvalid
	}
	raw, err := tokenEnc.DecodeString(tok)
	if err != nil || len(raw) != payloadLen+sha256.Size {
		return nil, errTokenInvalid
	}
	payload, mac := raw[:payloadLen], raw[payloadLen:]
	if !hmac.Equal(mac, s.tokenMAC(payload)) || payload[0] != purpose {
		return nil, errTokenInvalid
	}
	return payload, nil
}

// confirmToken is the signed confirmation token for subscriber id; it
// expires confirmTTL after now.
func (s *Service) confirmToken(id int64) string {
	p := make([]byte, 17)
	p[0] = tokenConfirm
	binary.BigEndian.PutUint64(p[1:], uint64(id))
	binary.BigEndian.PutUint64(p[9:], uint64(s.opts.Now().Add(confirmTTL).Unix()))
	return s.signToken(p)
}

// parseConfirmToken returns the subscriber id of a confirm token, or
// errTokenInvalid / errTokenExpired.
func (s *Service) parseConfirmToken(tok string) (int64, error) {
	p, err := s.openToken(tok, tokenConfirm, 17)
	if err != nil {
		return 0, err
	}
	exp := time.Unix(int64(binary.BigEndian.Uint64(p[9:])), 0)
	if s.opts.Now().After(exp) {
		return 0, errTokenExpired
	}
	return int64(binary.BigEndian.Uint64(p[1:])), nil
}

// unsubscribeToken is the signed, non-expiring unsubscribe token for id.
func (s *Service) unsubscribeToken(id int64) string {
	p := make([]byte, 9)
	p[0] = tokenUnsubscribe
	binary.BigEndian.PutUint64(p[1:], uint64(id))
	return s.signToken(p)
}

// parseUnsubscribeToken returns the subscriber id of an unsubscribe token,
// or errTokenInvalid.
func (s *Service) parseUnsubscribeToken(tok string) (int64, error) {
	p, err := s.openToken(tok, tokenUnsubscribe, 9)
	if err != nil {
		return 0, err
	}
	return int64(binary.BigEndian.Uint64(p[1:])), nil
}

// confirmLink and unsubscribeLink return absolute, signed URLs for sub on
// page (see pageBase).
func (s *Service) confirmLink(page model.StatusPage, sub model.Subscriber) string {
	return s.pageBase(page) + "/confirm?t=" + s.confirmToken(sub.ID)
}

func (s *Service) unsubscribeLink(page model.StatusPage, sub model.Subscriber) string {
	return s.pageBase(page) + "/unsubscribe?t=" + s.unsubscribeToken(sub.ID)
}
