package mcp

import (
	"context"
	"database/sql"
	"errors"

	"github.com/mark3labs/mcp-go/mcp"
)

// registerMediaTools wires the poll + contact-card tools.
func (s *Server) registerMediaTools() {
	s.registerSendPoll()
	s.registerSendPollVote()
	s.registerGetPollResults()
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
		if len(a.Options) > 32 {
			return mcp.NewToolResultError("too many options: max 32"), nil
		}
		r := s.client.SendPoll(ctx, a.Recipient, a.Question, a.Options, a.SelectableCount)
		return resultJSON(r)
	}))
}

type sendPollVoteArgs struct {
	ChatJID       string   `json:"chat_jid"`
	PollMessageID string   `json:"poll_message_id"`
	Options       []string `json:"options"`
}

func (s *Server) registerSendPollVote() {
	tool := mcp.NewTool("send_poll_vote",
		mcp.WithDescription("Cast a vote on a previously-seen poll. options must match the poll's option names exactly."),
		mcp.WithString("chat_jid", mcp.Required()),
		mcp.WithString("poll_message_id", mcp.Required()),
		mcp.WithArray("options", mcp.Required(), mcp.Items(map[string]any{"type": "string"})),
	)
	s.mcp.AddTool(tool, mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, a sendPollVoteArgs) (*mcp.CallToolResult, error) {
		if a.ChatJID == "" {
			return mcp.NewToolResultError("chat_jid is required"), nil
		}
		if a.PollMessageID == "" {
			return mcp.NewToolResultError("poll_message_id is required"), nil
		}
		if len(a.Options) == 0 {
			return mcp.NewToolResultError("options must not be empty"), nil
		}
		if len(a.Options) > 32 {
			return mcp.NewToolResultError("too many options: max 32"), nil
		}
		r := s.client.SendPollVote(ctx, a.ChatJID, a.PollMessageID, a.Options)
		return resultJSON(r)
	}))
}

type getPollResultsArgs struct {
	ChatJID       string `json:"chat_jid"`
	PollMessageID string `json:"poll_message_id"`
}

func (s *Server) registerGetPollResults() {
	tool := mcp.NewTool("get_poll_results",
		mcp.WithDescription("Return the current tally for a poll we have cached. Tally includes 0-vote options."),
		mcp.WithString("chat_jid", mcp.Required()),
		mcp.WithString("poll_message_id", mcp.Required()),
	)
	s.mcp.AddTool(tool, mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, a getPollResultsArgs) (*mcp.CallToolResult, error) {
		if a.ChatJID == "" {
			return mcp.NewToolResultError("chat_jid is required"), nil
		}
		if a.PollMessageID == "" {
			return mcp.NewToolResultError("poll_message_id is required"), nil
		}
		r, err := s.client.GetPollResults(ctx, a.ChatJID, a.PollMessageID)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return mcp.NewToolResultError("poll not found in cache — wait for message sync"), nil
			}
			return mcp.NewToolResultError(err.Error()), nil
		}
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
