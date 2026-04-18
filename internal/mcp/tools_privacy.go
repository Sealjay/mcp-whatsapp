package mcp

import (
	"context"
	"fmt"

	"github.com/mark3labs/mcp-go/mcp"
)

// registerPrivacyTools wires blocklist tools. Phase 3 will extend this file
// with the other privacy-related tools (privacy settings, status message).
func (s *Server) registerPrivacyTools() {
	s.registerGetBlocklist()
	s.registerBlockContact()
	s.registerUnblockContact()
}

func (s *Server) registerGetBlocklist() {
	tool := mcp.NewTool("get_blocklist",
		mcp.WithDescription("Return the user's current WhatsApp blocklist as JSON."),
	)
	s.mcp.AddTool(tool, func(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		js, err := s.client.GetBlocklist(ctx)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return mcp.NewToolResultText(js), nil
	})
}

type blocklistMutationArgs struct {
	JID string `json:"jid"`
}

func (s *Server) registerBlockContact() {
	tool := mcp.NewTool("block_contact",
		mcp.WithDescription("Block a contact by phone number or JID."),
		mcp.WithString("jid", mcp.Required()),
	)
	s.mcp.AddTool(tool, mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, a blocklistMutationArgs) (*mcp.CallToolResult, error) {
		if a.JID == "" {
			return mcp.NewToolResultError("jid is required"), nil
		}
		if err := s.client.BlockContact(ctx, a.JID); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return mcp.NewToolResultText(fmt.Sprintf("Blocked %s", a.JID)), nil
	}))
}

func (s *Server) registerUnblockContact() {
	tool := mcp.NewTool("unblock_contact",
		mcp.WithDescription("Unblock a contact by phone number or JID."),
		mcp.WithString("jid", mcp.Required()),
	)
	s.mcp.AddTool(tool, mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, a blocklistMutationArgs) (*mcp.CallToolResult, error) {
		if a.JID == "" {
			return mcp.NewToolResultError("jid is required"), nil
		}
		if err := s.client.UnblockContact(ctx, a.JID); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return mcp.NewToolResultText(fmt.Sprintf("Unblocked %s", a.JID)), nil
	}))
}
