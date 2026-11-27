package listings

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

type countingSigner struct {
	mu    sync.Mutex
	calls int
	err   error
}

func (signer *countingSigner) PresignDownload(_ context.Context, key string, _ time.Duration) (string, error) {
	signer.mu.Lock()
	defer signer.mu.Unlock()
	if signer.err != nil {
		return "", signer.err
	}
	signer.calls++
	return fmt.Sprintf("https://storage.test/%s?sig=%d", key, signer.calls), nil
}

func signedAt(t *testing.T, signer *ReusedPhotoSigner, key string, lifetime time.Duration) string {
	t.Helper()
	url, err := signer.PresignDownload(context.Background(), key, lifetime)
	if err != nil {
		t.Fatal(err)
	}
	return url
}

func TestPhotoLinkIsReusedOnlyWhileHalfItsLifetimeRemains(t *testing.T) {
	cases := []struct {
		name   string
		later  time.Duration
		reused bool
	}{
		{"immediately", 0, true},
		{"just before half", 30*time.Minute - time.Second, true},
		{"at half", 30 * time.Minute, false},
		{"after expiry", 2 * time.Hour, false},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			now := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
			signer := NewReusedPhotoSigner(&countingSigner{})
			signer.now = func() time.Time { return now }

			first := signedAt(t, signer, "listings/a/0.jpg", time.Hour)
			now = now.Add(test.later)
			second := signedAt(t, signer, "listings/a/0.jpg", time.Hour)

			if (first == second) != test.reused {
				t.Fatalf("reused = %v, want %v", first == second, test.reused)
			}
		})
	}
}

func TestPhotoLinksAreNotSharedAcrossKeysOrLifetimes(t *testing.T) {
	signer := NewReusedPhotoSigner(&countingSigner{})

	first := signedAt(t, signer, "listings/a/0.jpg", time.Hour)

	if first == signedAt(t, signer, "listings/a/1.jpg", time.Hour) {
		t.Fatal("two photos shared a link")
	}
	if first == signedAt(t, signer, "listings/a/0.jpg", time.Minute) {
		t.Fatal("a short-lived request received the long-lived link")
	}
}

func TestFailedPhotoSigningIsNotRemembered(t *testing.T) {
	source := &countingSigner{err: errors.New("no credentials")}
	signer := NewReusedPhotoSigner(source)

	if _, err := signer.PresignDownload(context.Background(), "listings/a/0.jpg", time.Hour); err == nil {
		t.Fatal("expected the signing failure to surface")
	}
	source.err = nil

	if signedAt(t, signer, "listings/a/0.jpg", time.Hour) == "" {
		t.Fatal("expected a link once signing recovered")
	}
}

func TestRememberedPhotoLinksStayBounded(t *testing.T) {
	signer := NewReusedPhotoSigner(&countingSigner{})

	for index := range maxReusedPhotoLinks + 10 {
		signedAt(t, signer, fmt.Sprintf("listings/a/%d.jpg", index), time.Hour)
	}

	if len(signer.links) > maxReusedPhotoLinks {
		t.Fatalf("remembered %d links, limit %d", len(signer.links), maxReusedPhotoLinks)
	}
}

func TestConcurrentRequestsForOnePhotoReceiveOneLink(t *testing.T) {
	signer := NewReusedPhotoSigner(&countingSigner{})
	urls := make([]string, 20)
	var group sync.WaitGroup
	for index := range urls {
		group.Add(1)
		go func() {
			defer group.Done()
			urls[index], _ = signer.PresignDownload(context.Background(), "listings/a/0.jpg", time.Hour)
		}()
	}
	group.Wait()

	for _, url := range urls {
		if url != urls[0] {
			t.Fatalf("got two links: %q and %q", urls[0], url)
		}
	}
}
