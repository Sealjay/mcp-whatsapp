package mcp

import (
	"context"

	"github.com/mark3labs/mcp-go/mcp"
)

// registerMediaTools wires send_poll and send_contact_card.
func (s *Server) registerMediaTools() {
	s.registerSendPoll()
	s.registerSendContactCard()
}

type sendPollArgs struct {
	Recipient       string   `json:"recipient"`
	Question        string   `json:"question"`
	Options         []string `json:"options"`
	SelectableCount int      `json:"selectable_count,omitempty"`
}

func (s *Server) registerSendPoll() {
	tool := mcp.NewTool("send_poll",
		mcp.WithDescription("Send a poll message with 2+ options. selectable_count controls how many options a voter may pick (defaults to 1)."),
		mcp.WithString("recipient", mcp.Required()),
		mcp.WithString("question", mcp.Required()),
		mcp.WithArray("options", mcp.Required(), mcp.Items(map[string]any{"type": "string"})),
		mcp.WithNumber("selectable_count", mcp.DefaultNumber(1)),
	)
	s.mcp.AddTool(tool, mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, a sendPollArgs) (*mcp.CallToolResult, error) {
		if a.Recipient == "" {
			return mcp.NewToolResultError("recipient is required"), nil
		}
		if a.Question == "" {
			return mcp.NewToolResultError("question is required"), nil
		}
		if len(a.Options) < 2 {
			return mcp.NewToolResultError("options must contain at least 2 entries"), nil
		}
		r := s.client.SendPoll(ctx, a.Recipient, a.Question, a.Options, a.SelectableCount)
		return resultJSON(r)
	}))
}

type sendContactCardArgs struct {
	Recipient string `json:"recipient"`
	Name      string `json:"name"`
	Phone     string `json:"phone,omitempty"`
	VCard     string `json:"vcard,omitempty"`
}

func (s *Server) registerSendContactCard() {
	tool := mcp.NewTool("send_contact_card",
		mcp.WithDescription("Send a contact card. If vcard is omitted, a minimal vCard 3.0 is synthesised from name + phone."),
		mcp.WithString("recipient", mcp.Required()),
		mcp.WithString("name", mcp.Required()),
		mcp.WithString("phone", mcp.Description("Phone number (digits only preferred); used to synthesise the vCard when vcard is not provided.")),
		mcp.WithString("vcard", mcp.Description("Raw vCard 3.0 string; if provided, name+phone synthesis is skipped.")),
	)
	s.mcp.AddTool(tool, mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, a sendContactCardArgs) (*mcp.CallToolResult, error) {
		if a.Recipient == "" {
			return mcp.NewToolResultError("recipient is required"), nil
		}
		if a.Name == "" && a.VCard == "" {
			return mcp.NewToolResultError("name is required when vcard is not provided"), nil
		}
		r := s.client.SendContactCard(ctx, a.Recipient, a.Name, a.Phone, a.VCard)
		return resultJSON(r)
	}))
}
