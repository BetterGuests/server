package publicweb

import (
	"errors"
	"net"
	"net/http"
	"net/netip"
	"syscall"
	"time"
)

const (
	connectDeadline = 5 * time.Second
	maxRedirects    = 3
)

var (
	ErrPrivateAddress = errors.New("address is not public")
	ErrUnsafeRedirect = errors.New("redirect is not allowed")
)

var sharedAddressSpace = netip.MustParsePrefix("100.64.0.0/10")

func NewClient(deadline time.Duration) *http.Client {
	dialer := &net.Dialer{Timeout: connectDeadline, Control: RefusePrivateAddress}
	return &http.Client{
		Timeout:   deadline,
		Transport: &http.Transport{DialContext: dialer.DialContext, TLSHandshakeTimeout: connectDeadline},
		CheckRedirect: func(request *http.Request, via []*http.Request) error {
			if len(via) > maxRedirects || request.URL.Scheme != "https" {
				return ErrUnsafeRedirect
			}
			return nil
		},
	}
}

func RefusePrivateAddress(_, address string, _ syscall.RawConn) error {
	target, err := netip.ParseAddrPort(address)
	if err != nil || !public(target.Addr()) {
		return ErrPrivateAddress
	}
	return nil
}

func public(address netip.Addr) bool {
	address = address.Unmap()
	return address.IsGlobalUnicast() && !address.IsPrivate() && !sharedAddressSpace.Contains(address)
}
