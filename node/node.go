package node

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"github.com/InazumaV/V2bX/api/panel"
	"github.com/InazumaV/V2bX/conf"
	vCore "github.com/InazumaV/V2bX/core"
)

type Node struct {
	controllers []*Controller
}

func New() *Node {
	return &Node{}
}

func (n *Node) Start(nodes []conf.NodeConfig, core vCore.Core) error {
	// Validate all effective tags before starting any controller. A duplicate
	// would merge limiter and traffic identities even across different Panels.
	controllers := make([]*Controller, len(nodes))
	tags := make([]string, len(nodes))
	seen := make(map[string]int, len(nodes))
	for i := range nodes {
		p, err := panel.New(&nodes[i].ApiConfig)
		if err != nil {
			return err
		}
		controllers[i] = NewController(core, p, &nodes[i].Options)
		tag := nodes[i].Options.Name
		if tag == "" {
			tag = controllers[i].buildNodeTag(&panel.NodeInfo{Type: p.NodeType, Id: p.NodeId})
		}
		if previous, exists := seen[tag]; exists {
			return fmt.Errorf("duplicate node identity in configuration entries %d and %d", previous+1, i+1)
		}
		seen[tag], tags[i] = i, tag
	}
	// Explicit names are also logged and used as identities. Reject a name or
	// host that would expose any configured Panel API key, including the keys
	// of other nodes. Do not include the conflicting value in the error.
	for i, tag := range tags {
		for j := range nodes {
			key := nodes[j].ApiConfig.Key
			if tagContainsCredential(tag, key) {
				return fmt.Errorf("node identity in configuration entry %d contains a Panel credential", i+1)
			}
		}
	}
	n.controllers = controllers
	for i := range nodes {
		err := n.controllers[i].Start()
		if err != nil {
			return fmt.Errorf("start node controller [%s-%d] error: %s",
				nodes[i].ApiConfig.NodeType,
				nodes[i].ApiConfig.NodeID,
				err)
		}
	}
	return nil
}

var percentEscapePattern = regexp.MustCompile(`%[0-9a-fA-F]{2}`)

func tagContainsCredential(tag, key string) bool {
	if key == "" {
		return false
	}
	if strings.Contains(tag, key) {
		return true
	}
	// Decode valid escapes without case-folding credential characters. Decode
	// each escape independently so an unrelated malformed '%' cannot bypass
	// the check. Query '+' decoding must happen before percent decoding.
	decode := func(value string) string {
		return percentEscapePattern.ReplaceAllStringFunc(value, func(escape string) string {
			decoded, _ := url.PathUnescape(escape)
			return decoded
		})
	}
	return strings.Contains(decode(tag), key) ||
		strings.Contains(decode(strings.ReplaceAll(tag, "+", " ")), key)
}

func (n *Node) Close() {
	for _, c := range n.controllers {
		err := c.Close()
		if err != nil {
			panic(err)
		}
	}
	n.controllers = nil
}
