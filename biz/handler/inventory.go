package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
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
		// decode first, filter → map → sort after: the envelope's three
		// counting fields come from render.ListSpec, so this list answers
		// "how many matched" the SAME way the management plane's
		// jms_assets_list does — the two tools are one list seen from two
		// sides, and this side used to report total as the UNFILTERED row
		// count and truncated=true whenever a filter removed anything.
		decoded := make([]map[string]any, 0, len(rows))
		for _, r := range rows {
			var a map[string]any
			if json.Unmarshal(r, &a) == nil {
				decoded = append(decoded, a)
			}
		}
		return render.Text(render.ListSpec[map[string]any, jms.AssetSummary]{
			Rows: decoded,
			Keep: func(a map[string]any) bool {
				if args.Platform != "" &&
					!strings.EqualFold(fmt.Sprint(jmsapi.Nested(a, "platform", "name")), args.Platform) {
					return false
				}
				if args.Search != "" {
					s := strings.ToLower(args.Search)
					name := strings.ToLower(fmt.Sprint(a["name"]))
					addr := strings.ToLower(fmt.Sprint(a["address"]))
					if !strings.Contains(name, s) && !strings.Contains(addr, s) {
						return false
					}
				}
				return true
			},
			Map: func(a map[string]any) jms.AssetSummary { return jmsapi.AssetSummary(a) },
			Less: func(x, y jms.AssetSummary) bool {
				return x.GetAddress() < y.GetAddress()
			},
			// No limit argument: the endpoint's page (page_size=500) is the
			// only cap, so Max<=0 — nothing here truncates on its own.
		}.Build())
	})
}
