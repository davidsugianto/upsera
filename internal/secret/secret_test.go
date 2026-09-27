package secret

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func mustBox(t *testing.T, s string) *Box {
	t.Helper()
	b, err := New(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestRoundTrip(t *testing.T) {
	b := mustBox(t, "app-secret-app-secret-app-secret-1")
	plain := []byte(`{"bot_token":"123:abc"}`)
	ct, err := b.Encrypt(plain)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(ct, "v1:") {
		t.Fatalf("ciphertext %q lacks v1: prefix", ct)
	}
	if strings.Contains(ct, "123:abc") {
		t.Fatal("ciphertext contains plaintext")
	}
	got, err := b.Decrypt(ct)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, plain) {
		t.Fatalf("got %q, want %q", got, plain)
	}
	ct2, _ := b.Encrypt(plain)
	if ct2 == ct {
		t.Fatal("two encryptions produced the same ciphertext (nonce reuse)")
	}
}

func TestDecryptErrors(t *testing.T) {
	b := mustBox(t, "app-secret-app-secret-app-secret-1")
	ct, err := b.Encrypt([]byte("hello world"))
	if err != nil {
		t.Fatal(err)
	}

	// Flip one byte of the payload (after the prefix), keeping valid base64.
	raw := []byte(ct)
	i := len("v1:") + 5
	if raw[i] == 'A' {
		raw[i] = 'B'
	} else {
		raw[i] = 'A'
	}

	other := mustBox(t, "a-completely-different-app-secret-2")
	cases := []struct {
		name string
		box  *Box
		in   string
		want error
	}{
		{"tampered", b, string(raw), ErrCorrupt},
		{"wrong key", other, ct, ErrCorrupt},
		{"unknown version", b, "v2:" + strings.TrimPrefix(ct, "v1:"), ErrUnknownVersion},
		{"no prefix", b, "plaintext", ErrUnknownVersion},
		{"bad base64", b, "v1:!!!", ErrCorrupt},
		{"too short", b, "v1:AAAA", ErrCorrupt},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := tc.box.Decrypt(tc.in); !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
}
