package model_test

import (
	"strings"
	"testing"

	"github.com/davidsugianto/upsera/internal/model"
)

func TestTruncateMessage(t *testing.T) {
	t.Run("short message unchanged", func(t *testing.T) {
		if got := model.TruncateMessage("ok"); got != "ok" {
			t.Fatalf("got %q, want %q", got, "ok")
		}
	})

	t.Run("NUL bytes stripped", func(t *testing.T) {
		got := model.TruncateMessage("hello\x00world")
		if got != "helloworld" {
			t.Fatalf("got %q, want %q", got, "helloworld")
		}
	})

	t.Run("invalid UTF-8 replaced", func(t *testing.T) {
		got := model.TruncateMessage("bad\xffbyte")
		want := "bad\uFFFDbyte"
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})

	t.Run("sanitized before truncation, not after", func(t *testing.T) {
		// 50 NUL bytes followed by exactly MaxMessageLen 'a's: the raw rune
		// count (250) exceeds MaxMessageLen, but once the NULs are stripped
		// the remaining 200 'a's fit exactly and must not be truncated or
		// carry any NUL bytes into the stored message. Truncating before
		// sanitizing would instead keep the leading NULs and cut valid
		// content short.
		msg := strings.Repeat("\x00", 50) + strings.Repeat("a", model.MaxMessageLen)
		got := model.TruncateMessage(msg)
		want := strings.Repeat("a", model.MaxMessageLen)
		if got != want {
			t.Fatalf("got %d runes (contains NUL: %v), want %d clean 'a' runes",
				len([]rune(got)), strings.ContainsRune(got, 0), model.MaxMessageLen)
		}
	})

	t.Run("still truncates long sanitized messages", func(t *testing.T) {
		got := model.TruncateMessage(strings.Repeat("a", model.MaxMessageLen+50))
		r := []rune(got)
		if len(r) != model.MaxMessageLen {
			t.Fatalf("len = %d, want %d", len(r), model.MaxMessageLen)
		}
		if r[len(r)-1] != '…' {
			t.Fatalf("last rune = %q, want ellipsis", r[len(r)-1])
		}
	})
}
