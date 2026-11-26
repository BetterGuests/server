package payments

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	vgsTokenURL         = "https://auth.verygoodsecurity.com/auth/realms/vgs/protocol/openid-connect/token"
	vgsSandboxURL       = "https://sandbox.vgsapi.com"
	vgsLiveURL          = "https://vgsapi.com"
	vgsSandbox          = "sandbox"
	vgsLive             = "live"
	vgsTimeout          = 5 * time.Second
	maxVGSResponseBytes = 1 << 20
)

var ErrIncompleteVGSConfig = errors.New("set every VGS variable or none of them")

type VGSCredentials struct {
	ClientID     string
	ClientSecret string
}

type VGSConfig struct {
	VaultID     string
	Environment string
	Server      VGSCredentials
	Browser     VGSCredentials
}

func VGSConfigFromLookup(lookup func(string) string) (VGSConfig, bool, error) {
	read := func(name string) string { return strings.TrimSpace(lookup(name)) }
	config := VGSConfig{
		VaultID:     read("VGS_VAULT_ID"),
		Environment: read("VGS_ENVIRONMENT"),
		Server:      VGSCredentials{read("VGS_CLIENT_ID"), read("VGS_CLIENT_SECRET")},
		Browser:     VGSCredentials{read("VGS_BROWSER_CLIENT_ID"), read("VGS_BROWSER_CLIENT_SECRET")},
	}
	if config == (VGSConfig{}) {
		return VGSConfig{}, false, nil
	}
	required := []string{
		config.VaultID, config.Server.ClientID, config.Server.ClientSecret,
		config.Browser.ClientID, config.Browser.ClientSecret,
	}
	for _, value := range required {
		if value == "" {
			return VGSConfig{}, false, ErrIncompleteVGSConfig
		}
	}
	if config.Environment != vgsSandbox && config.Environment != vgsLive {
		return VGSConfig{}, false, fmt.Errorf("VGS_ENVIRONMENT must be %q or %q", vgsSandbox, vgsLive)
	}
	return config, true, nil
}

type VGSClient struct {
	config   VGSConfig
	apiURL   string
	tokenURL string
	http     *http.Client
}

func NewVGSClient(config VGSConfig) *VGSClient {
	apiURL := vgsSandboxURL
	if config.Environment == vgsLive {
		apiURL = vgsLiveURL
	}
	return &VGSClient{config: config, apiURL: apiURL, tokenURL: vgsTokenURL, http: &http.Client{Timeout: vgsTimeout}}
}

func (client *VGSClient) CollectSession(ctx context.Context) (CollectSession, error) {
	token, err := client.token(ctx, client.config.Browser)
	if err != nil {
		return CollectSession{}, err
	}
	return CollectSession{VaultID: client.config.VaultID, Environment: client.config.Environment, AccessToken: token}, nil
}

type vgsCardResponse struct {
	Data struct {
		Attributes struct {
			Last4    string `json:"last4"`
			ExpMonth int    `json:"exp_month"`
			ExpYear  int    `json:"exp_year"`
		} `json:"attributes"`
	} `json:"data"`
	Included []struct {
		Type string `json:"type"`
		Meta struct {
			CardArt struct {
				Assets []vgsAsset `json:"assets"`
			} `json:"card_art"`
		} `json:"meta"`
	} `json:"included"`
}

type vgsAsset struct {
	Type        string `json:"type"`
	DownloadURL string `json:"download_url"`
}

func (response vgsCardResponse) artURL() string {
	for _, resource := range response.Included {
		if resource.Type != "network_tokens" {
			continue
		}
		for _, asset := range resource.Meta.CardArt.Assets {
			if asset.Type == "DIGITAL_CARD_ART" && strings.HasPrefix(asset.DownloadURL, "https://") {
				return asset.DownloadURL
			}
		}
	}
	return ""
}

func (client *VGSClient) Card(ctx context.Context, vaultCardID string) (VaultCard, error) {
	var response vgsCardResponse
	if err := client.call(ctx, http.MethodGet, "/cards/"+url.PathEscape(vaultCardID), &response); err != nil {
		return VaultCard{}, err
	}
	attributes := response.Data.Attributes
	return VaultCard{
		Last4:        attributes.Last4,
		ExpMonth:     attributes.ExpMonth,
		ShortExpYear: attributes.ExpYear % 100,
		ArtURL:       response.artURL(),
	}, nil
}

func (client *VGSClient) ProvisionToken(ctx context.Context, vaultCardID string) error {
	return client.call(ctx, http.MethodPost, "/cards/"+url.PathEscape(vaultCardID)+"/network-tokens", nil)
}

func (client *VGSClient) DeleteCard(ctx context.Context, vaultCardID string) error {
	return client.call(ctx, http.MethodDelete, "/cards/"+url.PathEscape(vaultCardID), nil)
}

func (client *VGSClient) token(ctx context.Context, credentials VGSCredentials) (string, error) {
	form := url.Values{
		"grant_type":    {"client_credentials"},
		"client_id":     {credentials.ClientID},
		"client_secret": {credentials.ClientSecret},
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, client.tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", fmt.Errorf("vgs token request: %w", err)
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	var response struct {
		AccessToken string `json:"access_token"`
	}
	if err := client.send(request, &response); err != nil {
		return "", fmt.Errorf("vgs token: %w", err)
	}
	if response.AccessToken == "" {
		return "", errors.New("vgs token: empty access token")
	}
	return response.AccessToken, nil
}

func (client *VGSClient) call(ctx context.Context, method, path string, into any) error {
	token, err := client.token(ctx, client.config.Server)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, method, client.apiURL+path, nil)
	if err != nil {
		return fmt.Errorf("vgs request: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	if err := client.send(request, into); err != nil {
		return fmt.Errorf("vgs %s: %w", method, err)
	}
	return nil
}

func (client *VGSClient) send(request *http.Request, into any) error {
	response, err := client.http.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("status %d", response.StatusCode)
	}
	if into == nil {
		return nil
	}
	return json.NewDecoder(io.LimitReader(response.Body, maxVGSResponseBytes)).Decode(into)
}
