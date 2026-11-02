package panel

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"

	"github.com/go-resty/resty/v2"
	"github.com/sirupsen/logrus"
)

// Retry/debug messages may contain URLs, headers, bodies, or server text. Never
// format their arguments: redacting just the token would miss other credentials.
type panelHTTPLogger struct{}

func (panelHTTPLogger) Errorf(string, ...interface{}) {
	logrus.Error("Panel HTTP client error (request details omitted)")
}

func (panelHTTPLogger) Warnf(string, ...interface{}) {
	logrus.Warn("Panel HTTP client warning (request details omitted)")
}

func (panelHTTPLogger) Debugf(string, ...interface{}) {
	logrus.Debug("Panel HTTP client debug event (request details omitted)")
}

// Debug retains events without dumping request or response contents.
func (c *Client) Debug() {
	c.client.SetDebug(true)
}

// Only controlled diagnostics may escape the HTTP client. Do not wrap original
// errors: their messages and unwrap chains may contain credentials.
func safeRequestError(err error) error {
	switch {
	case errors.Is(err, context.Canceled):
		return errors.New("Panel request canceled")
	case errors.Is(err, context.DeadlineExceeded):
		return errors.New("Panel request timed out")
	}
	var certErr *tls.CertificateVerificationError
	var unknownCA x509.UnknownAuthorityError
	var hostnameErr x509.HostnameError
	if errors.As(err, &certErr) || errors.As(err, &unknownCA) || errors.As(err, &hostnameErr) {
		return errors.New("Panel TLS certificate verification failed")
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return errors.New("Panel DNS lookup failed")
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return errors.New("Panel request timed out")
	}
	return errors.New("Panel request failed (request details omitted)")
}

func (c *Client) checkResponse(res *resty.Response, _ string, err error) error {
	if err != nil {
		return safeRequestError(err)
	}
	if res == nil || res.RawResponse == nil {
		return errors.New("Panel request failed: no HTTP response")
	}
	status := res.StatusCode()
	if status >= 300 && status < 400 && status != 304 {
		return fmt.Errorf("Panel request rejected: HTTP %d redirect; configure the final Panel URL", status)
	}
	if status >= 400 {
		// Bodies and reason phrases can reflect tokens or Authorization values.
		return fmt.Errorf("Panel request failed: HTTP %d", status)
	}
	return nil
}
