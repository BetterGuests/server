package pms

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	stateLifetime = 15 * time.Minute
	passLifetime  = time.Hour
	webReturnPath = "/hosting/connect/"
)

type SignInService struct {
	apps map[ProviderName]signInApp
	now  func() time.Time
}

func NewSignInService(apps []App) (*SignInService, error) {
	service := &SignInService{apps: map[ProviderName]signInApp{}, now: time.Now}
	for _, app := range apps {
		seal, err := sealerFor(app.ClientSecret)
		if err != nil {
			return nil, fmt.Errorf("seal %s connections: %w", app.Provider, err)
		}
		service.apps[app.Provider] = signInApp{authorizer: app.Authorizer, seal: seal}
	}
	return service, nil
}

func AppsFromLookup(lookup func(string) string) ([]App, error) {
	web := strings.TrimRight(strings.TrimSpace(lookup("APP_WEB_URL")), "/")
	var apps []App
	for _, known := range signInProviders {
		prefix := strings.ToUpper(string(known))
		id := strings.TrimSpace(lookup(prefix + "_CLIENT_ID"))
		secret := strings.TrimSpace(lookup(prefix + "_CLIENT_SECRET"))
		if id == "" && secret == "" {
			continue
		}
		if id == "" || secret == "" {
			return nil, fmt.Errorf("%s_CLIENT_ID and %s_CLIENT_SECRET must be set together", prefix, prefix)
		}
		if !safeWebAddress(web) {
			return nil, errors.New("APP_WEB_URL must be an https address, or localhost, to sign in to a property system")
		}
		redirect := web + webReturnPath + string(known)
		app := App{Provider: known, ClientSecret: secret}
		switch known {
		case Hospitable:
			source := NewHospitableSource(id, secret)
			app.Authorizer, app.Source = source, source
		case OwnerRez:
			source := NewOwnerRezSource(id, secret, redirect)
			app.Authorizer, app.Source = source, source
		}
		apps = append(apps, app)
	}
	return apps, nil
}

func safeWebAddress(address string) bool {
	parsed, err := url.Parse(address)
	if err != nil || parsed.Host == "" {
		return false
	}
	return parsed.Scheme == "https" || (parsed.Scheme == "http" && parsed.Hostname() == "localhost")
}

func (service *SignInService) Providers() []ProviderName {
	ready := []ProviderName{}
	for _, known := range signInProviders {
		if _, found := service.apps[known]; found {
			ready = append(ready, known)
		}
	}
	return ready
}

func (service *SignInService) Start(hostID uuid.UUID, provider ProviderName) (string, error) {
	app, found := service.apps[provider]
	if !found {
		return "", ErrUnknownProvider
	}
	state, err := app.seal.close(sealed{Purpose: purposeState, Provider: provider, Host: hostID, Expires: service.now().Add(stateLifetime)})
	if err != nil {
		return "", fmt.Errorf("start sign in: %w", err)
	}
	return app.authorizer.AuthorizeURL(state), nil
}

func (service *SignInService) Finish(ctx context.Context, hostID uuid.UUID, provider ProviderName, code, state string) (string, error) {
	app, found := service.apps[provider]
	if !found {
		return "", ErrUnknownProvider
	}
	if _, err := service.open(hostID, provider, purposeState, state); err != nil {
		return "", err
	}
	if code == "" || len(code) > maxCredentialLength {
		return "", ErrSignInExpired
	}
	ctx, cancel := context.WithTimeout(ctx, providerDeadline)
	defer cancel()
	token, err := app.authorizer.Exchange(ctx, code)
	if err != nil {
		if errors.Is(err, ErrCredentialsRefused) {
			return "", ErrSignInExpired
		}
		return "", fmt.Errorf("finish %s sign in: %w", provider, err)
	}
	return app.seal.close(sealed{Purpose: purposePass, Provider: provider, Host: hostID, Expires: service.now().Add(passLifetime), Token: token})
}

func (service *SignInService) Token(hostID uuid.UUID, provider ProviderName, pass string) (string, error) {
	opened, err := service.open(hostID, provider, purposePass, pass)
	return opened.Token, err
}

func (service *SignInService) Handles(provider ProviderName) bool {
	return slices.Contains(signInProviders, provider)
}

func (service *SignInService) open(hostID uuid.UUID, provider ProviderName, purpose, value string) (sealed, error) {
	app, found := service.apps[provider]
	if !found {
		return sealed{}, ErrUnknownProvider
	}
	opened, err := app.seal.open(value)
	if err != nil || opened.Purpose != purpose || opened.Provider != provider || opened.Host != hostID || !service.now().Before(opened.Expires) {
		return sealed{}, ErrSignInExpired
	}
	return opened, nil
}

type sealer struct {
	box cipher.AEAD
}

func sealerFor(secret string) (sealer, error) {
	derived := hmac.New(sha256.New, []byte(secret))
	_, _ = derived.Write([]byte("better-guests property system sign in"))
	block, err := aes.NewCipher(derived.Sum(nil))
	if err != nil {
		return sealer{}, err
	}
	box, err := cipher.NewGCMWithRandomNonce(block)
	return sealer{box: box}, err
}

func (seal sealer) close(content sealed) (string, error) {
	plain, err := json.Marshal(content)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(seal.box.Seal(nil, nil, plain, nil)), nil
}

func (seal sealer) open(value string) (sealed, error) {
	var content sealed
	data, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return content, err
	}
	plain, err := seal.box.Open(nil, nil, data, nil)
	if err != nil {
		return content, err
	}
	return content, json.Unmarshal(plain, &content)
}
