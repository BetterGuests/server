package calendars

import (
	"context"
	"fmt"
	"io"
	"net/http"
)

type WebFetcher struct {
	client *http.Client
}

func NewWebFetcher(client *http.Client) *WebFetcher {
	return &WebFetcher{client: client}
}

func (fetcher *WebFetcher) Fetch(ctx context.Context, address string) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return nil, ErrInvalidAddress
	}
	request.Header.Set("Accept", "text/calendar, text/plain;q=0.8")
	request.Header.Set("User-Agent", "BetterGuests-Calendar/1.0")
	response, err := fetcher.client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrUnreachable, err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: status %d", ErrUnreachable, response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxFeedBytes+1))
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrUnreachable, err)
	}
	if len(data) > maxFeedBytes {
		return nil, ErrTooLarge
	}
	return data, nil
}
