package panel

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/InazumaV/V2bX/conf"
	"github.com/sirupsen/logrus"
	"github.com/vmihailenco/msgpack/v5"
)

const securityToken = "fixture-panel-key-ONLY-+/%&=secret"
const securityAuthorization = "fixture-authorization-ONLY-secret"

// Capture both logrus and Resty's independent stderr logger, including stdout
// diagnostics. Tests using this helper must not run in parallel.
func captureSecurityLogs(t *testing.T) func() string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	out, stderr := os.Stdout, os.Stderr
	logger := logrus.StandardLogger()
	logOut, level := logger.Out, logger.GetLevel()
	os.Stdout, os.Stderr = w, w
	logger.SetOutput(w)
	logger.SetLevel(logrus.DebugLevel)
	var buf bytes.Buffer
	done := make(chan struct{})
	go func() {
		_, _ = io.Copy(&buf, r)
		close(done)
	}()
	var once sync.Once
	finish := func() string {
		once.Do(func() {
			os.Stdout, os.Stderr = out, stderr
			logger.SetOutput(logOut)
			logger.SetLevel(level)
			_ = w.Close()
			<-done
			_ = r.Close()
		})
		return buf.String()
	}
	t.Cleanup(func() { finish() })
	return finish
}

func assertSecurityDiagnostics(t *testing.T, text string) {
	t.Helper()
	for _, secret := range []string{
		securityToken, url.QueryEscape(securityToken), url.PathEscape(securityToken),
		securityAuthorization, "fixture-url-password", "fixture-response-password",
		"Authorization:", "Bearer ", "token=", "https://", "http://",
	} {
		if strings.Contains(text, secret) {
			t.Error("credential or raw request/response content escaped into diagnostics")
		}
	}
}

func securityClient(t *testing.T, host string, server *httptest.Server) *Client {
	t.Helper()
	c, err := New(&conf.ApiConfig{APIHost: host, Key: securityToken, NodeType: "vless", NodeID: 7, Timeout: 1})
	if err != nil {
		t.Fatal("could not construct fixture client")
	}
	if server != nil {
		transport := server.Client().Transport.(*http.Transport).Clone()
		if transport.TLSClientConfig != nil && transport.TLSClientConfig.InsecureSkipVerify {
			t.Fatal("test trust must use a CA, not skip TLS verification")
		}
		c.client.SetTransport(transport)
		t.Cleanup(transport.CloseIdleConnections)
	}
	c.client.SetRetryWaitTime(time.Millisecond).SetRetryMaxWaitTime(time.Millisecond)
	c.client.SetHeader("Authorization", "Bearer "+securityAuthorization)
	c.Debug()
	return c
}

func securityCall(c *Client, endpoint string) error {
	switch endpoint {
	case "config":
		_, err := c.GetNodeInfo()
		return err
	case "user":
		_, err := c.GetUserList()
		return err
	case "alivelist":
		_, err := c.GetUserAlive()
		return err
	case "push":
		return c.ReportUserTraffic([]UserTraffic{{UID: 42, Upload: 123, Download: 456}})
	case "alive":
		data := map[int][]string{42: {"192.0.2.1"}}
		return c.ReportNodeOnlineUsers(&data)
	default:
		panic("unknown fixture endpoint")
	}
}

func TestPanelSecurityRedirects(t *testing.T) {
	finish := captureSecurityLogs(t)
	var outsideHits atomic.Int32
	outside := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		outsideHits.Add(1)
	}))
	defer outside.Close()
	outsideHTTP := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		outsideHits.Add(1)
	}))
	defer outsideHTTP.Close()
	for _, code := range []int{301, 302, 303, 307, 308} {
		for _, destination := range []string{outside.URL + "/landing", "/same-origin", outsideHTTP.URL + "/downgrade", "https://bad.invalid/%zz"} {
			t.Run(fmt.Sprintf("status-%d-destination-%d", code, len(destination)), func(t *testing.T) {
				var unexpected atomic.Int32
				s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if !strings.HasPrefix(r.URL.Path, "/api/v1/server/UniProxy/") {
						unexpected.Add(1)
					}
					w.Header().Set("Location", destination)
					w.WriteHeader(code)
					_, _ = io.WriteString(w, "Authorization: Bearer "+securityAuthorization+" token="+securityToken)
				}))
				defer s.Close()
				c := securityClient(t, s.URL, s)
				for _, endpoint := range []string{"config", "user", "push", "alive", "alivelist"} {
					err := securityCall(c, endpoint)
					if err == nil {
						t.Error("redirect was reported as a successful API request")
					}
					if !strings.Contains(destination, "%zz") && (err == nil || !strings.Contains(err.Error(), "redirect")) {
						t.Error("valid redirect was not rejected as a redirect")
					}
					if err != nil {
						assertSecurityDiagnostics(t, fmt.Sprintf("%v %+v %#v", err, err, err))
					}
				}
				if unexpected.Load() != 0 || outsideHits.Load() != 0 {
					t.Error("redirect request left the configured endpoint")
				}
				if c.responseBodyHash != "" || c.nodeEtag != "" {
					t.Error("rejected response changed node cache")
				}
			})
		}
	}
	assertSecurityDiagnostics(t, finish())
}

func TestPanelSecurityHTTPAndDecodeErrors(t *testing.T) {
	finish := captureSecurityLogs(t)
	for _, status := range []int{200, 401, 403, 500, 503} {
		for _, contentType := range []string{"application/json", "application/x-msgpack"} {
			t.Run(fmt.Sprintf("status-%d-%s", status, contentType), func(t *testing.T) {
				s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", contentType)
					w.WriteHeader(status)
					_, _ = io.WriteString(w, `{"users":[{"id":"`+securityToken+`"}],"Authorization":"Bearer `+securityAuthorization+`","password":"fixture-response-password"}`)
				}))
				defer s.Close()
				c := securityClient(t, s.URL, s)
				for _, endpoint := range []string{"config", "user", "alivelist", "push", "alive"} {
					err := securityCall(c, endpoint)
					if endpoint == "user" || endpoint == "alivelist" || status >= 400 {
						if err == nil {
							t.Error("invalid response was not rejected")
						}
					}
					if err != nil {
						assertSecurityDiagnostics(t, fmt.Sprintf("%v %+v %#v", err, err, err))
					}
				}
			})
		}
	}
	assertSecurityDiagnostics(t, finish())
}

type securityRoundTripper func(*http.Request) (*http.Response, error)

func (f securityRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestPanelSecurityNetworkAndInvalidRequests(t *testing.T) {
	finish := captureSecurityLogs(t)
	for _, requestErr := range []error{
		errors.New("token=" + securityToken + " Authorization: Bearer " + securityAuthorization),
		&net.DNSError{Name: securityToken},
		context.DeadlineExceeded,
		context.Canceled,
	} {
		c := securityClient(t, "https://panel.example.invalid", nil)
		var attempts int
		c.client.SetTransport(securityRoundTripper(func(r *http.Request) (*http.Response, error) {
			attempts++
			return nil, requestErr
		}))
		for _, endpoint := range []string{"config", "user", "push", "alive", "alivelist"} {
			err := securityCall(c, endpoint)
			if err == nil {
				t.Error("network failure was not returned")
			}
			if err != nil {
				assertSecurityDiagnostics(t, fmt.Sprintf("%v %+v %#v", err, err, err))
				if errors.Unwrap(err) != nil {
					t.Error("raw error remained accessible in unwrap chain")
				}
			}
		}
		if attempts < 4 {
			t.Error("retry path was not exercised")
		}
	}
	for _, host := range []string{"", "/relative?token=" + securityToken, "https://fixture-url-password@%zz", "https://panel.invalid/\nAuthorization: " + securityAuthorization, "ftp://panel.invalid"} {
		_, err := New(&conf.ApiConfig{APIHost: host, Key: securityToken, NodeType: "vless"})
		if err == nil {
			t.Error("invalid Panel URL was accepted")
		} else {
			assertSecurityDiagnostics(t, fmt.Sprintf("%v %+v %#v", err, err, err))
		}
	}
	headerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("invalid header reached the server")
	}))
	defer headerServer.Close()
	c := securityClient(t, headerServer.URL, headerServer)
	c.client.SetHeader("Authorization", "Bearer "+securityAuthorization+"\n")
	if err := c.ReportUserTraffic(nil); err == nil {
		t.Error("invalid header was accepted")
	} else {
		assertSecurityDiagnostics(t, err.Error())
	}
	assertSecurityDiagnostics(t, finish())
}

func TestPanelSecurityTLSVerification(t *testing.T) {
	finish := captureSecurityLogs(t)
	var hits atomic.Int32
	s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1) }))
	defer s.Close()
	c := securityClient(t, s.URL, nil) // Do NOT trust the test CA in this case.
	err := c.ReportUserTraffic(nil)
	if err == nil || !strings.Contains(err.Error(), "certificate verification") {
		t.Error("an untrusted TLS certificate was not rejected with a safe diagnostic")
	}
	if hits.Load() != 0 {
		t.Error("HTTP request reached a server with an untrusted certificate")
	}
	if err != nil {
		assertSecurityDiagnostics(t, err.Error())
	}
	assertSecurityDiagnostics(t, finish())
}

func TestPanelSecurityDirectAPICompatibility(t *testing.T) {
	finish := captureSecurityLogs(t)
	var requests atomic.Int32
	users := UserListBody{Users: []UserInfo{{Id: 42, Uuid: "fixture-uuid", SpeedLimit: 10, DeviceLimit: 2}}}
	var userMsgpack atomic.Bool
	s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.URL.Query().Get("token") != securityToken || r.URL.Query().Get("node_type") != "vless" || r.URL.Query().Get("node_id") != "7" {
			t.Error("API authentication or node query changed")
		}
		if r.Header.Get("Authorization") != "Bearer "+securityAuthorization {
			t.Error("Authorization header changed")
		}
		switch r.URL.Path {
		case "/api/v1/server/UniProxy/config":
			if r.Header.Get("If-None-Match") == "fixture-etag" {
				w.WriteHeader(http.StatusNotModified)
				return
			}
			w.Header().Set("ETag", "fixture-etag")
			_, _ = io.WriteString(w, `{"server_port":443,"network":"tcp","tls":2,"flow":"xtls-rprx-vision","encryption":"mlkem768x25519plus","base_config":{"push_interval":60,"pull_interval":60}}`)
		case "/api/v1/server/UniProxy/user":
			if r.Header.Get("X-Response-Format") != "msgpack" {
				t.Error("user format negotiation changed")
			}
			if userMsgpack.Load() {
				w.Header().Set("Content-Type", "application/x-msgpack")
				_ = msgpack.NewEncoder(w).Encode(users)
			} else {
				_ = json.NewEncoder(w).Encode(users)
			}
		case "/api/v1/server/UniProxy/alivelist":
			_, _ = io.WriteString(w, `{"alive":{"42":2}}`)
		case "/api/v1/server/UniProxy/push":
			var body map[int][]int64
			if r.Method != http.MethodPost || json.NewDecoder(r.Body).Decode(&body) != nil || len(body[42]) != 2 || body[42][0] != 123 || body[42][1] != 456 {
				t.Error("traffic endpoint or counters changed")
			}
		case "/api/v1/server/UniProxy/alive":
			var body map[int][]string
			if r.Method != http.MethodPost || json.NewDecoder(r.Body).Decode(&body) != nil || len(body[42]) != 1 || body[42][0] != "192.0.2.1" {
				t.Error("online user payload changed")
			}
		default:
			t.Error("API endpoint changed")
		}
		if !strings.HasSuffix(r.URL.Path, "/push") && !strings.HasSuffix(r.URL.Path, "/alive") && r.Method != http.MethodGet {
			t.Error("API GET method changed")
		}
	}))
	defer s.Close()
	c := securityClient(t, s.URL, s)
	node, err := c.GetNodeInfo()
	if err != nil || node == nil || node.VAllss.Flow != "xtls-rprx-vision" || node.VAllss.Encryption != "mlkem768x25519plus" || node.Security != Reality || node.Common.ServerPort != 443 {
		t.Fatal("direct node configuration response changed")
	}
	if node, err = c.GetNodeInfo(); err != nil || node != nil {
		t.Error("ETag/304 behavior changed")
	}
	for _, format := range []string{"json", "msgpack"} {
		userMsgpack.Store(format == "msgpack")
		got, err := c.GetUserList()
		if err != nil || len(got) != 1 || got[0] != users.Users[0] {
			t.Error("user data changed")
		}
	}
	if alive, err := c.GetUserAlive(); err != nil || alive[42] != 2 {
		t.Error("alive list changed")
	}
	for _, endpoint := range []string{"push", "alive"} {
		if securityCall(c, endpoint) != nil {
			t.Error("direct report request failed")
		}
	}
	if requests.Load() != 7 {
		t.Error("unexpected request count")
	}
	assertSecurityDiagnostics(t, finish())
}

func TestPanelSecurityInvalidConfigurationDoesNotPoisonCache(t *testing.T) {
	finish := captureSecurityLogs(t)
	s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", "invalid-fixture-etag")
		_, _ = io.WriteString(w, `{"server_port":"`+securityToken+`"}`)
	}))
	defer s.Close()
	c := securityClient(t, s.URL, s)
	for range 2 {
		_, err := c.GetNodeInfo()
		if err == nil {
			t.Error("invalid node response was accepted as unchanged")
		} else {
			assertSecurityDiagnostics(t, err.Error())
		}
	}
	if c.responseBodyHash != "" || c.nodeEtag != "" {
		t.Error("invalid configuration changed cache validators")
	}
	assertSecurityDiagnostics(t, finish())
}

func TestPanelSecurityRealTimeout(t *testing.T) {
	finish := captureSecurityLogs(t)
	s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		select {
		case <-r.Context().Done():
		case <-time.After(100 * time.Millisecond):
		}
	}))
	defer s.Close()
	c := securityClient(t, s.URL, s)
	c.client.SetTimeout(20 * time.Millisecond)
	err := c.ReportUserTraffic(nil)
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Error("real request timeout did not produce a safe timeout diagnostic")
	}
	if err != nil {
		assertSecurityDiagnostics(t, err.Error())
	}
	assertSecurityDiagnostics(t, finish())
}

func TestPanelSecurityURLCredentialsStayOutOfDiagnostics(t *testing.T) {
	finish := captureSecurityLogs(t)
	var authOK atomic.Bool
	s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, password, ok := r.BasicAuth()
		authOK.Store(ok && user == "fixture-user" && password == "fixture-url-password")
		w.WriteHeader(http.StatusForbidden)
	}))
	defer s.Close()
	base := strings.Replace(s.URL, "https://", "https://fixture-user:fixture-url-password@", 1)
	c := securityClient(t, base, s)
	c.client.Header.Del("Authorization")
	err := c.ReportUserTraffic(nil)
	if err == nil || !authOK.Load() {
		t.Error("URL-based authentication was not preserved")
	}
	if strings.Contains(c.APIHost, "@") || !strings.HasPrefix(c.APIHost, s.URL+"#panel-id=") {
		t.Error("display host can leak URL credentials into node tags")
	}
	if err != nil {
		assertSecurityDiagnostics(t, err.Error())
	}
	assertSecurityDiagnostics(t, finish())
}

func TestPanelSecurityDistinctNodeIdentities(t *testing.T) {
	finish := captureSecurityLogs(t)
	hosts := []string{
		"https://tenant-a:fixture-url-password@panel.example.invalid",
		"https://tenant-b:fixture-url-password@panel.example.invalid",
		"https://tenant-a:fixture-response-password@panel.example.invalid",
		"https://panel.example.invalid?token=" + url.QueryEscape(securityToken),
		"https://panel.example.invalid?other=" + url.QueryEscape(securityAuthorization),
		"https://panel.example.invalid/" + url.PathEscape(securityToken),
		"https://another.example.invalid",
		"https://panel.example.invalid",
	}
	identities := make(map[string]bool)
	for _, host := range hosts {
		c, err := New(&conf.ApiConfig{APIHost: host, Key: securityToken, NodeType: "vless", NodeID: 7})
		if err != nil {
			t.Fatal("valid fixture Panel URL was rejected")
		}
		assertSecurityDiagnostics(t, strings.TrimPrefix(c.APIHost, "https://"))
		if strings.Contains(c.APIHost, "tenant-a") || strings.Contains(c.APIHost, "tenant-b") || strings.Contains(c.APIHost, "@") || strings.Contains(c.APIHost, "?") {
			t.Error("node identity exposed URL credentials or query")
		}
		if identities[c.APIHost] {
			t.Error("distinct Panel URLs generated the same node identity")
		}
		identities[c.APIHost] = true
		again, err := New(&conf.ApiConfig{APIHost: host, Key: securityToken, NodeType: "vless", NodeID: 7})
		if err != nil || again.APIHost != c.APIHost {
			t.Error("node identity is not stable")
		}
		if c.client.BaseURL != host {
			t.Error("underlying Panel request URL was modified")
		}
	}
	assertSecurityDiagnostics(t, finish())
}

func TestPanelSecurityAliveErrorsRetainLastGoodState(t *testing.T) {
	finish := captureSecurityLogs(t)
	var status atomic.Int32
	status.Store(http.StatusOK)
	var body atomic.Value
	body.Store(`{"alive":{"42":2}}`)
	var disconnect atomic.Bool
	var redirected atomic.Int32
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { redirected.Add(1) }))
	defer target.Close()
	s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if disconnect.Load() {
			conn, _, err := w.(http.Hijacker).Hijack()
			if err == nil {
				_ = conn.Close()
			}
			return
		}
		if status.Load() == http.StatusFound {
			w.Header().Set("Location", target.URL+"/redirected")
		}
		w.WriteHeader(int(status.Load()))
		_, _ = io.WriteString(w, body.Load().(string))
	}))
	defer s.Close()
	c := securityClient(t, s.URL, s)
	alive, err := c.GetUserAlive()
	if err != nil || alive[42] != 2 {
		t.Fatal("valid alive list failed")
	}
	previous := c.AliveMap
	for _, code := range []int{301, 302, 307, 401, 403, 500, 503, 304, 204} {
		status.Store(int32(code))
		body.Store("token=" + securityToken + " Authorization: Bearer " + securityAuthorization)
		got, err := c.GetUserAlive()
		if err == nil || got != nil {
			t.Error("non-success alive response did not return an error")
		}
		if err != nil {
			assertSecurityDiagnostics(t, fmt.Sprintf("%v %+v %#v", err, err, err))
		}
		if c.AliveMap != previous || previous.Alive[42] != 2 {
			t.Error("failed response replaced the last authoritative alive list")
		}
	}
	status.Store(http.StatusOK)
	for _, invalid := range []string{`{"alive":null}`, `{}`, `{"alive":{"42":"` + securityToken + `"}}`, `{"alive":{"42":3},"bad":`} {
		body.Store(invalid)
		got, err := c.GetUserAlive()
		if err == nil || got != nil {
			t.Error("invalid alive payload was accepted")
		}
		if err != nil {
			assertSecurityDiagnostics(t, err.Error())
		}
		if c.AliveMap != previous || previous.Alive[42] != 2 {
			t.Error("partial decode mutated the previous alive list")
		}
	}
	disconnect.Store(true)
	if got, err := c.GetUserAlive(); err == nil || got != nil {
		t.Error("connection failure was treated as an empty success")
	}
	if c.AliveMap != previous || previous.Alive[42] != 2 {
		t.Error("connection failure changed alive state")
	}
	disconnect.Store(false)
	body.Store(`{"alive":{}}`)
	if got, err := c.GetUserAlive(); err != nil || got == nil || len(got) != 0 {
		t.Error("authoritative empty alive map was rejected")
	}
	body.Store(`{"alive":{"42":1}}`)
	if got, err := c.GetUserAlive(); err != nil || got[42] != 1 {
		t.Error("alive recovery failed")
	}
	if redirected.Load() != 0 {
		t.Error("alive request followed a redirect")
	}
	assertSecurityDiagnostics(t, finish())
}
