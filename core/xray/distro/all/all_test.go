package all_test

import (
	"encoding/json"
	"fmt"
	"testing"

	_ "github.com/InazumaV/V2bX/core/xray/distro/all"
	"github.com/xtls/xray-core/infra/conf"
	"github.com/xtls/xray-core/transport/internet"
	"github.com/xtls/xray-core/transport/internet/kcp"
	"github.com/xtls/xray-core/transport/internet/tcp"
)

func TestTCPHeaderRegistration(t *testing.T) {
	for _, header := range []string{"none", "http"} {
		t.Run(header, func(t *testing.T) {
			config := &conf.TCPConfig{HeaderConfig: json.RawMessage(fmt.Sprintf(`{"type":%q}`, header))}
			built, err := config.Build()
			if err != nil {
				t.Fatal(err)
			}
			settings := built.(*tcp.Config)
			headerConfig, err := settings.HeaderSettings.GetInstance()
			if err != nil {
				t.Fatal(err)
			}
			if _, err := internet.CreateConnectionAuthenticator(headerConfig); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestKCPHeaderRegistration(t *testing.T) {
	for _, header := range []string{"none", "srtp", "dtls", "utp", "wechat-video", "wireguard"} {
		t.Run(header, func(t *testing.T) {
			config := &conf.KCPConfig{HeaderConfig: json.RawMessage(fmt.Sprintf(`{"type":%q}`, header))}
			built, err := config.Build()
			if err != nil {
				t.Fatal(err)
			}
			settings := built.(*kcp.Config)
			headerConfig, err := settings.HeaderConfig.GetInstance()
			if err != nil {
				t.Fatal(err)
			}
			if _, err := internet.CreatePacketHeader(headerConfig); err != nil {
				t.Fatal(err)
			}
		})
	}
}
