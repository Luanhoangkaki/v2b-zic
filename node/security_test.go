package node

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/InazumaV/V2bX/api/panel"
	"github.com/InazumaV/V2bX/common/format"
	"github.com/InazumaV/V2bX/conf"
	vCore "github.com/InazumaV/V2bX/core"
	"github.com/InazumaV/V2bX/limiter"
)

const nodeSecurityToken = "fixture-node-api-key-not-real"

type nodeSecurityCore struct {
	addedTag, deletedTag string
	users                []panel.UserInfo
	trafficTag           string
	resetTraffic         bool
}

func (*nodeSecurityCore) Start() error { return nil }
func (*nodeSecurityCore) Close() error { return nil }
func (c *nodeSecurityCore) AddNode(tag string, _ *panel.NodeInfo, _ *conf.Options) error {
	c.addedTag = tag
	return nil
}
func (c *nodeSecurityCore) DelNode(tag string) error {
	c.deletedTag = tag
	return nil
}
func (c *nodeSecurityCore) AddUsers(p *vCore.AddUsersParams) (int, error) {
	if p.Tag != c.addedTag || p.Type != "vless" {
		return 0, fmt.Errorf("unexpected fixture user node")
	}
	c.users = append([]panel.UserInfo(nil), p.Users...)
	return len(c.users), nil
}
func (c *nodeSecurityCore) GetUserTrafficSlice(tag string, reset bool) ([]panel.UserTraffic, error) {
	c.trafficTag, c.resetTraffic = tag, reset
	return []panel.UserTraffic{{UID: 42, Upload: 123, Download: 456}}, nil
}
func (*nodeSecurityCore) DelUsers([]panel.UserInfo, string, *panel.NodeInfo) error { return nil }
func (*nodeSecurityCore) Protocols() []string                                      { return []string{"vless"} }
func (*nodeSecurityCore) Type() string                                             { return "xray" }

var _ vCore.Core = (*nodeSecurityCore)(nil)

func TestNodeSecurityStartupAndTrafficCompatibility(t *testing.T) {
	limiter.Init()
	var reports atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("token") != nodeSecurityToken || r.URL.Query().Get("node_type") != "vless" || r.URL.Query().Get("node_id") != "7" {
			t.Error("node API authentication changed")
		}
		switch r.URL.Path {
		case "/api/v1/server/UniProxy/config":
			_, _ = io.WriteString(w, `{"server_port":443,"network":"tcp","tls":2,"base_config":{"push_interval":3600,"pull_interval":3600}}`)
		case "/api/v1/server/UniProxy/user":
			_, _ = io.WriteString(w, `{"users":[{"id":42,"uuid":"fixture-user-uuid","speed_limit":10,"device_limit":2}]}`)
		case "/api/v1/server/UniProxy/alivelist":
			_, _ = io.WriteString(w, `{"alive":{"42":0}}`)
		case "/api/v1/server/UniProxy/push":
			var data map[int][]int64
			if r.Method != http.MethodPost || json.NewDecoder(r.Body).Decode(&data) != nil || len(data[42]) != 2 || data[42][0] != 123 || data[42][1] != 456 {
				t.Error("node traffic values or method changed")
			}
			reports.Add(1)
		default:
			t.Error("unexpected node API endpoint")
		}
	}))
	defer s.Close()
	c := &nodeSecurityCore{}
	n := New()
	err := n.Start([]conf.NodeConfig{{
		ApiConfig: conf.ApiConfig{APIHost: s.URL, Key: nodeSecurityToken, NodeID: 7, NodeType: "vless", Timeout: 1},
		Options:   conf.Options{Core: "xray", CertConfig: &conf.CertConfig{CertMode: "none"}},
	}}, c)
	if err != nil {
		t.Fatal("node startup failed against the fixture Panel")
	}
	defer n.Close()
	expectedTag := fmt.Sprintf("[%s]-vless:7", s.URL)
	if c.addedTag != expectedTag || len(c.users) != 1 || c.users[0] != (panel.UserInfo{Id: 42, Uuid: "fixture-user-uuid", SpeedLimit: 10, DeviceLimit: 2}) {
		t.Fatal("node tag or authentication/limit data passed to the core changed")
	}
	l, err := limiter.GetLimiter(expectedTag)
	if err != nil || l.UUIDtoUID["fixture-user-uuid"] != 42 {
		t.Fatal("user limiter identity changed")
	}
	if _, rejected := l.CheckLimit(format.UserTag(expectedTag, "unknown-fixture-user"), "192.0.2.2", true, false); !rejected {
		t.Error("an unknown user was accepted by the limiter")
	}
	if _, rejected := l.CheckLimit(format.UserTag(expectedTag, "fixture-user-uuid"), "192.0.2.1", true, false); rejected {
		t.Error("the authorized fixture user was rejected")
	}
	if err := n.controllers[0].reportUserTrafficTask(); err != nil {
		t.Error("node report task failed")
	}
	if c.trafficTag != expectedTag || !c.resetTraffic || reports.Load() != 1 {
		t.Error("traffic lookup/reset/report behavior changed")
	}
}

func TestNodeSecurityStartupErrorDoesNotExposeHost(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, r.URL.String()+" "+nodeSecurityToken)
	}))
	defer s.Close()
	base := strings.Replace(s.URL, "http://", "http://fixture-user:fixture-password@", 1)
	c := &nodeSecurityCore{}
	n := New()
	err := n.Start([]conf.NodeConfig{{ApiConfig: conf.ApiConfig{
		APIHost: base, Key: nodeSecurityToken, NodeID: 7, NodeType: "vless", Timeout: 1,
	}}}, c)
	if err == nil {
		t.Fatal("unauthorized Panel request was not rejected")
	}
	for _, secret := range []string{nodeSecurityToken, "fixture-password", "http://", "token="} {
		if strings.Contains(err.Error(), secret) {
			t.Error("startup diagnostic exposed URL credentials")
		}
	}
	if c.addedTag != "" || len(c.users) != 0 || c.trafficTag != "" {
		t.Error("failed startup altered the core or queried traffic")
	}
}

func TestNodeSecurityDistinctPanelIdentitiesAndLimiter(t *testing.T) {
	limiter.Init()
	addresses := []string{
		"https://tenant-one:fixture-password-one@panel.example.invalid",
		"https://tenant-two:fixture-password-two@panel.example.invalid",
		"https://panel.example.invalid?token=fixture-query-token-one",
		"https://panel.example.invalid?token=fixture-query-token-two",
	}
	tags := make(map[string]int)
	for i, address := range addresses {
		client, err := panel.New(&conf.ApiConfig{APIHost: address, Key: nodeSecurityToken, NodeID: 7, NodeType: "vless"})
		if err != nil {
			t.Fatal("valid fixture Panel URL was rejected")
		}
		controller := NewController(&nodeSecurityCore{}, client, &conf.Options{})
		tag := controller.buildNodeTag(&panel.NodeInfo{Type: "vless", Id: 7})
		if _, exists := tags[tag]; exists {
			t.Fatal("different Panel APIHosts produced the same node tag")
		}
		tags[tag] = 100 + i
		for _, secret := range []string{nodeSecurityToken, "tenant-one", "tenant-two", "fixture-password", "fixture-query-token", "token="} {
			if strings.Contains(tag, secret) {
				t.Error("node tag exposed credentials")
			}
		}
		users := []panel.UserInfo{{Id: 100 + i, Uuid: "same-fixture-uuid"}}
		l := limiter.AddLimiter(tag, &conf.LimitConfig{}, users, nil)
		t.Cleanup(func() { limiter.DeleteLimiter(tag) })
		if l.UUIDtoUID["same-fixture-uuid"] != 100+i {
			t.Error("limiter stored the wrong Panel user")
		}
	}
	for tag, uid := range tags {
		l, err := limiter.GetLimiter(tag)
		if err != nil || l == nil || l.UUIDtoUID["same-fixture-uuid"] != uid {
			t.Error("distinct Panel limiter was overwritten")
		}
	}
}

func TestNodeSecurityRejectsConflictingOrSecretNamesBeforeStartup(t *testing.T) {
	var requests atomic.Int32
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1) })
	first := httptest.NewServer(handler)
	defer first.Close()
	second := httptest.NewServer(handler)
	defer second.Close()
	config := func(host, name, key string) conf.NodeConfig {
		return conf.NodeConfig{
			ApiConfig: conf.ApiConfig{APIHost: host, Key: key, NodeID: 7, NodeType: "vless", Timeout: 1},
			Options:   conf.Options{Name: name},
		}
	}
	const otherToken = "fixture-other-panel-key-not-real"
	const escapedToken = "fixture/+Case Sensitive%Token"
	lowerHexEscapes := strings.NewReplacer("%2F", "%2f", "%2B", "%2b").Replace(url.QueryEscape(escapedToken))
	autoTag := fmt.Sprintf("[%s]-vless:7", first.URL)
	tests := []struct {
		name  string
		nodes []conf.NodeConfig
	}{
		{"same explicit name", []conf.NodeConfig{config(first.URL, "duplicate-name", nodeSecurityToken), config(second.URL, "duplicate-name", otherToken)}},
		{"same automatic tag", []conf.NodeConfig{config(first.URL, "", nodeSecurityToken), config(first.URL, "", otherToken)}},
		{"explicit name matches automatic tag", []conf.NodeConfig{config(first.URL, "", nodeSecurityToken), config(second.URL, autoTag, otherToken)}},
		{"own token in name", []conf.NodeConfig{config(first.URL, "tag-"+nodeSecurityToken, nodeSecurityToken)}},
		{"other Panel token in name", []conf.NodeConfig{config(first.URL, "tag-"+otherToken, nodeSecurityToken), config(second.URL, "distinct-name", otherToken)}},
		{"query-encoded token", []conf.NodeConfig{config(first.URL, "tag-"+url.QueryEscape(escapedToken), escapedToken)}},
		{"path-encoded token", []conf.NodeConfig{config(first.URL, "tag-"+url.PathEscape(escapedToken), escapedToken)}},
		{"lower-case hex escapes", []conf.NodeConfig{config(first.URL, "tag-"+lowerHexEscapes, escapedToken)}},
		{"unrelated malformed percent", []conf.NodeConfig{config(first.URL, "invalid%zz-tag-"+lowerHexEscapes, escapedToken)}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			core := &nodeSecurityCore{}
			n := New()
			err := n.Start(tc.nodes, core)
			if err == nil {
				t.Fatal("unsafe node identity was accepted")
			}
			for _, secret := range []string{nodeSecurityToken, otherToken, first.URL, second.URL} {
				if strings.Contains(err.Error(), secret) {
					t.Error("validation error exposed a credential or raw URL")
				}
			}
			if core.addedTag != "" || len(core.users) != 0 || core.trafficTag != "" || requests.Load() != 0 {
				t.Error("validation failure reached a Panel or started a node")
			}
		})
	}
}

func TestNodeSecurityCaseSensitiveKeyAndAliveFailure(t *testing.T) {
	for _, tc := range []struct{ name, key, label string }{
		{"protocol differs in case", "VLESS", ""},
		{"explicit name differs in case", "FixtureCaseSensitiveABC", "node-fixturecasesensitiveabc"},
		{"percent hex text is not credential text", "AB", "node-%ab"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			limiter.Init()
			var aliveFailure atomic.Bool
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Query().Get("token") != tc.key {
					t.Error("API Key was modified")
				}
				switch r.URL.Path {
				case "/api/v1/server/UniProxy/config":
					if r.Header.Get("If-None-Match") == "fixture-etag" {
						w.WriteHeader(http.StatusNotModified)
						return
					}
					w.Header().Set("ETag", "fixture-etag")
					_, _ = io.WriteString(w, `{"server_port":443,"network":"tcp","tls":0,"base_config":{"push_interval":3600,"pull_interval":3600}}`)
				case "/api/v1/server/UniProxy/user":
					_, _ = io.WriteString(w, `{"users":[{"id":42,"uuid":"fixture-user-uuid","device_limit":3}]}`)
				case "/api/v1/server/UniProxy/alivelist":
					if aliveFailure.Load() {
						w.WriteHeader(http.StatusServiceUnavailable)
						return
					}
					_, _ = io.WriteString(w, `{"alive":{"42":2}}`)
				default:
					t.Error("unexpected fixture endpoint")
				}
			}))
			defer s.Close()
			n := New()
			core := &nodeSecurityCore{}
			cfg := []conf.NodeConfig{{ApiConfig: conf.ApiConfig{APIHost: s.URL, Key: tc.key, NodeID: 7, NodeType: "vless", Timeout: 1}, Options: conf.Options{Name: tc.label, CertConfig: &conf.CertConfig{CertMode: "none"}}}}
			if err := n.Start(cfg, core); err != nil {
				t.Fatalf("valid case-sensitive key rejected: %v", err)
			}
			defer n.Close()
			controller := n.controllers[0]
			oldAlive := controller.limiter.AliveList
			aliveFailure.Store(true)
			_ = controller.nodeInfoMonitor()
			if controller.limiter.AliveList[42] != 2 || oldAlive[42] != 2 {
				t.Error("alive error cleared the limiter")
			}
			if core.deletedTag != "" {
				t.Error("alive failure restarted the active node")
			}
			second := New()
			secondCore := &nodeSecurityCore{}
			if err := second.Start(cfg, secondCore); err == nil {
				t.Error("new node started without an authoritative alive list")
			}
			if secondCore.addedTag != "" || len(secondCore.users) != 0 {
				t.Error("failed alive startup modified the core")
			}
		})
	}
}

func TestNodeSecurityAliveRecoveryDoesNotConsumePendingUpdates(t *testing.T) {
	limiter.Init()
	var revision atomic.Int32
	var aliveFailure atomic.Bool
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("token") != nodeSecurityToken {
			t.Error("API authentication changed")
		}
		switch r.URL.Path {
		case "/api/v1/server/UniProxy/config", "/api/v1/server/UniProxy/user":
			current := revision.Load()
			etag := fmt.Sprintf("fixture-%d", current)
			if r.Header.Get("If-None-Match") == etag {
				w.WriteHeader(http.StatusNotModified)
				return
			}
			w.Header().Set("ETag", etag)
			if strings.HasSuffix(r.URL.Path, "/config") {
				port := 443
				if current != 0 {
					port = 8443
				}
				_, _ = fmt.Fprintf(w, `{"server_port":%d,"network":"tcp","tls":0,"base_config":{"push_interval":3600,"pull_interval":3600}}`, port)
			} else {
				_, _ = fmt.Fprintf(w, `{"users":[{"id":%d,"uuid":"fixture-user-%d","device_limit":3}]}`, 42+current, current)
			}
		case "/api/v1/server/UniProxy/alivelist":
			if aliveFailure.Load() {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			_, _ = io.WriteString(w, `{"alive":{"42":2,"43":1}}`)
		default:
			t.Error("unexpected fixture endpoint")
		}
	}))
	defer s.Close()
	n := New()
	core := &nodeSecurityCore{}
	if err := n.Start([]conf.NodeConfig{{ApiConfig: conf.ApiConfig{APIHost: s.URL, Key: nodeSecurityToken, NodeID: 7, NodeType: "vless", Timeout: 1}, Options: conf.Options{CertConfig: &conf.CertConfig{CertMode: "none"}}}}, core); err != nil {
		t.Fatal("fixture startup failed")
	}
	defer n.Close()
	c := n.controllers[0]
	revision.Store(1)
	aliveFailure.Store(true)
	_ = c.nodeInfoMonitor()
	if c.info.VAllss.ServerPort != 443 || len(core.users) != 1 || core.users[0].Id != 42 || core.deletedTag != "" || c.limiter.AliveList[42] != 2 {
		t.Fatal("failed alive poll changed active config, users, or limiter")
	}
	aliveFailure.Store(false)
	_ = c.nodeInfoMonitor()
	if c.info.VAllss.ServerPort != 8443 || len(core.users) != 1 || core.users[0].Id != 43 || c.limiter.UUIDtoUID["fixture-user-1"] != 43 {
		t.Fatal("recovery skipped the pending config or user update")
	}
	if _, exists := c.limiter.UUIDtoUID["fixture-user-0"]; exists {
		t.Error("recovery retained the revoked fixture user")
	}
}
