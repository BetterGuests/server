package pms

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"syscall"
	"time"
)

const (
	photoDownloadDeadline = 30 * time.Second
	maxPhotoRedirects     = 3
)

var (
	errPrivateAddress = errors.New("photo address is not public")
	errPhotoRefused   = errors.New("photo could not be downloaded")
)

var sharedAddressSpace = netip.MustParsePrefix("100.64.0.0/10")

type WebPhotoFetcher struct {
	client *http.Client
}

func NewWebPhotoFetcher() *WebPhotoFetcher {
	dialer := &net.Dialer{Timeout: 5 * time.Second, Control: refusePrivateAddress}
	return &WebPhotoFetcher{client: &http.Client{
		Timeout:   photoDownloadDeadline,
		Transport: &http.Transport{DialContext: dialer.DialContext, TLSHandshakeTimeout: 5 * time.Second},
		CheckRedirect: func(request *http.Request, via []*http.Request) error {
			if len(via) > maxPhotoRedirects || request.URL.Scheme != "https" {
				return errPhotoRefused
			}
			return nil
		},
	}}
}

func refusePrivateAddress(_, address string, _ syscall.RawConn) error {
	target, err := netip.ParseAddrPort(address)
	if err != nil || !publicAddress(target.Addr()) {
		return errPrivateAddress
	}
	return nil
}

func publicAddress(address netip.Addr) bool {
	address = address.Unmap()
	return address.IsGlobalUnicast() && !address.IsPrivate() && !sharedAddressSpace.Contains(address)
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
