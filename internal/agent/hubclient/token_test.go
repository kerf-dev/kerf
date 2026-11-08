package hubclient

import (
	"bytes"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRequiredToken(t *testing.T) {
	const source = "Secret kerf/kerf-agent-token key token"
	for _, tc := range []struct {
		name    string
		content *string // nil: no file
		want    string
		missing bool
	}{
		{name: "present", content: ptr("tok-123\n"), want: "tok-123"},
		{name: "no file (Secret absent)", missing: true},
		{name: "empty file", content: ptr(" \n"), missing: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "token")
			if tc.content != nil {
				if err := os.WriteFile(path, []byte(*tc.content), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			var buf bytes.Buffer
			r := NewRequiredToken(FileToken(path), source, slog.New(slog.NewTextHandler(&buf, nil)))
			now := time.Unix(1000, 0)
			r.now = func() time.Time { return now }
			tok, err := r.Token()
			checkErr := r.Check(nil)
			if !tc.missing {
				if err != nil || checkErr != nil || tok != tc.want {
					t.Fatalf("Token() = %q, %v; Check() = %v; want %q", tok, err, checkErr, tc.want)
				}
				if buf.Len() != 0 {
					t.Errorf("logged %q", buf.String())
				}
				return
			}
			var m *MissingTokenError
			if !errors.As(err, &m) || err.Error() != "hub token missing: "+source || checkErr == nil {
				t.Fatalf("Token() error %v, Check() %v; want hub token missing", err, checkErr)
			}
			// Logged at error level once within logEvery, then again after it.
			if n := strings.Count(buf.String(), "level=ERROR msg=\"hub token missing: "+source+"\""); n != 1 {
				t.Errorf("logged %d times, want 1: %s", n, buf.String())
			}
			now = now.Add(11 * time.Second)
			_, _ = r.Token()
			if n := strings.Count(buf.String(), "hub token missing"); n != 2 {
				t.Errorf("after logEvery: logged %d times, want 2", n)
			}
		})
	}
}

func ptr(s string) *string { return &s }
