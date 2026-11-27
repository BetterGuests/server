package pms

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sync/errgroup"
)

const (
	apiRequestDeadline    = 15 * time.Second
	maxAPIResponseBytes   = 16 << 20
	parallelDetailLookups = 5
)

func newAPIClient() *http.Client {
	return &http.Client{
		Timeout: apiRequestDeadline,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func fetchJSON(client *http.Client, request *http.Request, into any) error {
	request.Header.Set("Accept", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrProviderFailed, err)
	}
	defer func() { _ = response.Body.Close() }()
	switch {
	case response.StatusCode == http.StatusUnauthorized, response.StatusCode == http.StatusForbidden,
		response.StatusCode == http.StatusBadRequest && request.Method == http.MethodPost:
		return ErrCredentialsRefused
	case response.StatusCode == http.StatusTooManyRequests:
		return ErrProviderBusy
	case response.StatusCode != http.StatusOK:
		return fmt.Errorf("%w: status %d", ErrProviderFailed, response.StatusCode)
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, maxAPIResponseBytes)).Decode(into); err != nil {
		return fmt.Errorf("%w: decode: %w", ErrProviderFailed, err)
	}
	return nil
}

func getJSON(ctx context.Context, client *http.Client, address string, headers map[string]string, into any) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrProviderFailed, err)
	}
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	return fetchJSON(client, request, into)
}

func fetchToken(client *http.Client, request *http.Request, into *tokenResponse) error {
	err := fetchJSON(client, request, into)
	if err != nil {
		return err
	}
	if into.AccessToken == "" {
		return fmt.Errorf("%w: no access token", ErrProviderFailed)
	}
	return nil
}

func withScheme(address string) string {
	if strings.HasPrefix(address, "//") {
		return "https:" + address
	}
	return address
}

func completeEach(ctx context.Context, properties []Property, complete func(context.Context, *Property) error) {
	var group errgroup.Group
	group.SetLimit(parallelDetailLookups)
	for index := range properties {
		group.Go(func() error {
			return complete(ctx, &properties[index])
		})
	}
	_ = group.Wait()
}

func (value *flexibleNumber) UnmarshalJSON(data []byte) error {
	text := strings.Trim(string(data), `"`)
	if text == "" || text == "null" {
		return nil
	}
	number, err := strconv.ParseFloat(text, 64)
	if err != nil {
		return nil
	}
	*value = flexibleNumber(number)
	return nil
}

func (value *flexibleID) UnmarshalJSON(data []byte) error {
	text := strings.Trim(string(data), `"`)
	if text != "null" {
		*value = flexibleID(text)
	}
	return nil
}

func firstFilled(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func cents(amount flexibleNumber) int {
	const largest = 10_000_000
	if amount <= 0 || amount > largest/100 {
		return 0
	}
	return int(float64(amount)*100 + 0.5)
}
