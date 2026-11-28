package pms

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
)

var errPhotoRefused = errors.New("photo could not be downloaded")

type WebPhotoFetcher struct {
	client *http.Client
}

func NewWebPhotoFetcher(client *http.Client) *WebPhotoFetcher {
	return &WebPhotoFetcher{client: client}
}

func (fetcher *WebPhotoFetcher) Fetch(ctx context.Context, address string) ([]byte, string, error) {
	target, err := url.Parse(withScheme(address))
	if err != nil || target.Scheme != "https" || target.Host == "" {
		return nil, "", errPhotoRefused
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return nil, "", errPhotoRefused
	}
	response, err := fetcher.client.Do(request)
	if err != nil {
		return nil, "", fmt.Errorf("download photo: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("%w: status %d", errPhotoRefused, response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxPhotoBytes+1))
	if err != nil {
		return nil, "", fmt.Errorf("read photo: %w", err)
	}
	if len(data) == 0 || len(data) > maxPhotoBytes {
		return nil, "", fmt.Errorf("%w: size", errPhotoRefused)
	}
	return data, http.DetectContentType(data), nil
}
