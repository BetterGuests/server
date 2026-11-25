package proofs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	personaFileDomain = ".withpersona.com"
	fileFetchTimeout  = 20 * time.Second
)

var ErrUntrustedFileURL = errors.New("document URL is not a Persona address")

type PersonaFileFetcher struct {
	client *http.Client
}

func NewPersonaFileFetcher(client *http.Client) *PersonaFileFetcher {
	if client == nil {
		client = &http.Client{Timeout: fileFetchTimeout}
	}
	return &PersonaFileFetcher{client: client}
}

func (fetcher *PersonaFileFetcher) Fetch(ctx context.Context, address string, maxBytes int64) ([]byte, error) {
	parsed, err := url.Parse(address)
	if err != nil || parsed.Scheme != "https" || !strings.HasSuffix(parsed.Hostname(), personaFileDomain) {
		return nil, ErrUntrustedFileURL
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return nil, errors.New("build document request")
	}
	response, err := fetcher.client.Do(request)
	if err != nil {
		var urlError *url.Error
		if errors.As(err, &urlError) {
			err = urlError.Err
		}
		return nil, fmt.Errorf("fetch document: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch document: status %d", response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read document: %w", err)
	}
	if int64(len(data)) > maxBytes {
		return nil, ErrDocumentRejected
	}
	return data, nil
}
