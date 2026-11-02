package panel

import (
	"crypto/sha256"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/InazumaV/V2bX/conf"
	"github.com/go-resty/resty/v2"
)

// Panel is the interface for different panel's api.

type Client struct {
	client           *resty.Client
	APIHost          string
	APISendIP        string
	Token            string
	NodeType         string
	NodeId           int
	nodeEtag         string
	userEtag         string
	responseBodyHash string
	UserList         *UserListBody
	AliveMap         *AliveMap
}

func New(c *conf.ApiConfig) (*Client, error) {
	// url.Parse errors can contain credentials from ApiHost.
	u, err := url.Parse(c.APIHost)
	if err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") || u.Opaque != "" {
		return nil, fmt.Errorf("invalid Panel URL: expected an absolute HTTP or HTTPS URL")
	}
	var client *resty.Client
	if c.APISendIP != "" {
		client = resty.NewWithLocalAddr(&net.TCPAddr{
			IP: net.ParseIP(c.APISendIP),
		})
	} else {
		client = resty.New()
	}
	// Reject all redirects, including same-origin redirects and HTTPS downgrades.
	// Keep the 3xx for checkResponse without a credential-bearing url.Error.
	client.GetClient().CheckRedirect = func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	}
	client.SetLogger(panelHTTPLogger{})
	client.SetRetryCount(3)
	if c.Timeout > 0 {
		client.SetTimeout(time.Duration(c.Timeout) * time.Second)
	} else {
		client.SetTimeout(5 * time.Second)
	}
	client.OnError(func(_ *resty.Request, err error) {
		logrus.Error(safeRequestError(err))
	})
	client.SetBaseURL(c.APIHost)
	// Check node type
	c.NodeType = strings.ToLower(c.NodeType)
	switch c.NodeType {
	case "v2ray":
		c.NodeType = "vmess"
	case
		"vmess",
		"trojan",
		"shadowsocks",
		"hysteria",
		"hysteria2",
		"tuic",
		"anytls",
		"vless":
	default:
		return nil, fmt.Errorf("unsupported Panel node type")
	}
	// set params
	client.SetQueryParams(map[string]string{
		"node_type": c.NodeType,
		"node_id":   strconv.Itoa(c.NodeID),
		"token":     c.Key,
	})
	// APIHost is used as an internal node identity and in logs. Retain the
	// original value for ordinary URLs. For URLs containing credentials, keep
	// a stable distinct identity without placing those credentials in the tag.
	apiHost := nodeIdentityHost(c.APIHost, u, c.Key)
	return &Client{
		client:    client,
		Token:     c.Key,
		APIHost:   apiHost,
		APISendIP: c.APISendIP,
		NodeType:  c.NodeType,
		NodeId:    c.NodeID,
		UserList:  &UserListBody{},
		AliveMap:  &AliveMap{},
	}, nil
}

func nodeIdentityHost(raw string, u *url.URL, token string) string {
	hasToken := token != "" && (strings.Contains(raw, token) || strings.Contains(u.Path, token) || strings.Contains(raw, url.PathEscape(token)))
	if u.User == nil && u.RawQuery == "" && !u.ForceQuery && u.Fragment == "" && u.RawFragment == "" && !hasToken {
		return raw
	}
	identity := sha256.Sum256([]byte(raw))
	safe := *u
	safe.User = nil
	safe.RawQuery = ""
	safe.ForceQuery = false
	safe.Fragment = ""
	safe.RawFragment = ""
	if token != "" && strings.Contains(safe.Path, token) {
		safe.Path = strings.ReplaceAll(safe.Path, token, "[redacted]")
		safe.RawPath = ""
	}
	display := safe.String()
	if token != "" {
		display = strings.ReplaceAll(display, token, "[redacted]")
		display = strings.ReplaceAll(display, url.PathEscape(token), "[redacted]")
	}
	return fmt.Sprintf("%s#panel-id=%x", display, identity)
}
