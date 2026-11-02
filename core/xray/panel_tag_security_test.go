package xray

import (
	"fmt"
	"strings"
	"testing"

	"github.com/InazumaV/V2bX/api/panel"
	"github.com/InazumaV/V2bX/common/counter"
	"github.com/InazumaV/V2bX/common/format"
	"github.com/InazumaV/V2bX/conf"
	"github.com/InazumaV/V2bX/core/xray/app/dispatcher"
)

func TestPanelSecurityDistinctTagsIsolateXrayTraffic(t *testing.T) {
	const token = "fixture-api-key-not-real"
	const uuid = "same-fixture-user-uuid"
	hosts := []string{
		"https://tenant-one:fixture-password-one@panel.example.invalid",
		"https://tenant-two:fixture-password-two@panel.example.invalid",
		"https://panel.example.invalid?token=fixture-query-token-one",
		"https://panel.example.invalid?token=fixture-query-token-two",
	}
	x := &Xray{
		dispatcher:                &dispatcher.DefaultDispatcher{},
		users:                     &UserMap{uidMap: make(map[string]int)},
		nodeReportMinTrafficBytes: make(map[string]int64),
	}
	tags := make([]string, len(hosts))
	seen := make(map[string]bool)
	for i, host := range hosts {
		c, err := panel.New(&conf.ApiConfig{APIHost: host, Key: token, NodeID: 7, NodeType: "vless"})
		if err != nil {
			t.Fatal("fixture Panel client could not be created")
		}
		// This is the same tag format validated against Controller.buildNodeTag
		// by node's TestNodeSecurityDistinctPanelIdentitiesAndLimiter.
		tag := fmt.Sprintf("[%s]-%s:%d", c.APIHost, c.NodeType, c.NodeId)
		if seen[tag] {
			t.Fatal("distinct Panel URLs have a conflicting traffic identity")
		}
		seen[tag], tags[i] = true, tag
		for _, secret := range []string{token, "tenant-one", "tenant-two", "fixture-password", "fixture-query-token", "token="} {
			if strings.Contains(tag, secret) {
				t.Error("traffic identity exposed credentials")
			}
		}
		userTag := format.UserTag(tag, uuid)
		x.users.uidMap[userTag] = 100 + i
		traffic := counter.NewTrafficCounter()
		storage := traffic.GetCounter(userTag)
		storage.UpCounter.Add(int64(11 + i))
		storage.DownCounter.Add(int64(21 + i))
		x.dispatcher.Counter.Store(tag, traffic)
	}
	for i, tag := range tags {
		traffic, err := x.GetUserTrafficSlice(tag, true)
		if err != nil || len(traffic) != 1 || traffic[0] != (panel.UserTraffic{UID: 100 + i, Upload: int64(11 + i), Download: int64(21 + i)}) {
			t.Errorf("Panel %d returned another Panel's counters or wrong values", i)
		}
		traffic, err = x.GetUserTrafficSlice(tag, false)
		if err != nil || len(traffic) != 0 {
			t.Errorf("Panel %d counters did not reset independently", i)
		}
	}
}
