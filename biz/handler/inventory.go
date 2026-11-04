package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/yi-nology/jumpserver-kit/model/jms"
	"github.com/yi-nology/jumpserver-kit/pkg/jmsapi"
	"github.com/yi-nology/jumpserver-kit/pkg/reg"
	"github.com/yi-nology/jumpserver-kit/pkg/render"
)

// The inventory tool: what the bastion holds, read over the REST API.
// It is how an agent finds the exact asset name before it opens a session.
//
// The rows are the SHARED asset row — the same one the management plane's
// jms_assets_list hands out. Both tools read the same bastion records and
// answer the same question, so they have to answer it in the same words: an
// agent that asked one server must not have to learn a second spelling.
func RegisterInventoryTools(r *reg.Registry, c *jmsapi.Client) {
	type invArgs struct {
		Platform string `json:"platform,omitempty" jsonschema:"filter by platform, e.g. Linux, Gateway, Kubernetes"`
		Search   string `json:"search,omitempty" jsonschema:"substring match on name or address"`
	}
	reg.Add(r, &mcp.Tool{
		Name:         "jmsx_inventory",
		Description:  "List assets known to JumpServer with address, platform and connectivity. Use this to discover the exact asset name before calling jmsx_exec. It is the execution plane's view of the same assets the management plane's jms_assets_list lists.",
		OutputSchema: render.Schema[render.ListOf[jms.AssetSummary]]()}, func(ctx context.Context, _ *mcp.CallToolRequest, args invArgs) (*mcp.CallToolResult, any, error) {
		var raw json.RawMessage
		if err := c.GetQuery(ctx, jmsapi.ResAssets, url.Values{"page_size": {"500"}}, &raw); err != nil {
			return render.Failf("%v", err)
		}
		rows, err := jmsapi.List(raw)
		if err != nil {
			return render.Failf("%v", err)
		}
		out := []jms.AssetSummary{}
		for _, r := range rows {
			var a map[string]any
			if json.Unmarshal(r, &a) != nil {
				continue
			}
			plat := fmt.Sprint(jmsapi.Nested(a, "platform", "name"))
			name := fmt.Sprint(a["name"])
			addr := fmt.Sprint(a["address"])
			if args.Platform != "" && !strings.EqualFold(plat, args.Platform) {
				continue
			}
			if args.Search != "" {
				s := strings.ToLower(args.Search)
				if !strings.Contains(strings.ToLower(name), s) && !strings.Contains(strings.ToLower(addr), s) {
					continue
				}
			}
			out = append(out, jmsapi.AssetSummary(a))
		}
		sort.Slice(out, func(i, j int) bool {
			return out[i].GetAddress() < out[j].GetAddress()
		})
		return render.Text(render.ListOf[jms.AssetSummary]{
			ListMeta: jms.ListMeta{
				Count:     int32(len(out)),
				Total:     int32(len(rows)),
				Truncated: len(out) < len(rows),
			},
			Items: out,
		})
	})
}
