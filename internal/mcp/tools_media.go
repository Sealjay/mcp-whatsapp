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
		mcp.WithDescription("Send a poll message with between 2 and 32 options. `selectable_count` controls how many options each voter may pick."),
		mcp.WithString("recipient", mcp.Required(), mcp.Description(recipientDesc)),
		mcp.WithString("question", mcp.Required(), mcp.Description("poll question")),
		mcp.WithArray("options", mcp.Required(), mcp.Items(map[string]any{"type": "string"}), mcp.Description("poll options (2–32)")),
		mcp.WithNumber("selectable_count", mcp.DefaultNumber(1), mcp.Description("how many options each voter may pick; 1 = single-choice")),
		mcp.WithDestructiveHintAnnotation(false),
	)
	s.mcp.AddTool(tool, mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, a sendPollArgs) (*mcp.CallToolResult, error) {
		if r := requireNonEmpty("recipient", a.Recipient); r != nil {
			return r, nil
		}
		if r := requireNonEmpty("question", a.Question); r != nil {
			return r, nil
		}
		if len(a.Options) < 2 {
			return mcp.NewToolResultError("options: need at least 2 entries"), nil
		}
		if len(a.Options) > 32 {
			return mcp.NewToolResultError("options: max 32 entries"), nil
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
		mcp.WithDescription("Cast (or re-cast) a vote on a previously-seen poll. Each call replaces the caller's prior vote on that poll; `options` must match the poll's option names exactly."),
		mcp.WithString("chat_jid", mcp.Required(), mcp.Description(jidDesc)),
		mcp.WithString("poll_message_id", mcp.Required(), mcp.Description("ID of the poll message to vote on")),
		mcp.WithArray("options", mcp.Required(), mcp.Items(map[string]any{"type": "string"}), mcp.Description("option names to pick (1–32); must match the poll exactly")),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
	)
	s.mcp.AddTool(tool, mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, a sendPollVoteArgs) (*mcp.CallToolResult, error) {
		if r := requireNonEmpty("chat_jid", a.ChatJID); r != nil {
			return r, nil
		}
		if r := requireNonEmpty("poll_message_id", a.PollMessageID); r != nil {
			return r, nil
		}
		if len(a.Options) == 0 {
			return mcp.NewToolResultError("options: required"), nil
		}
		if len(a.Options) > 32 {
			return mcp.NewToolResultError("options: max 32 entries"), nil
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
		mcp.WithDescription(offlineSafePrefix+"Return the current tally for a cached poll. Zero-vote options are included."),
		mcp.WithString("chat_jid", mcp.Required(), mcp.Description(jidDesc)),
		mcp.WithString("poll_message_id", mcp.Required(), mcp.Description("ID of the poll message to tally")),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(false),
	)
	s.mcp.AddTool(tool, mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, a getPollResultsArgs) (*mcp.CallToolResult, error) {
		if r := requireNonEmpty("chat_jid", a.ChatJID); r != nil {
			return r, nil
		}
		if r := requireNonEmpty("poll_message_id", a.PollMessageID); r != nil {
			return r, nil
		}
		r, err := s.client.GetPollResults(ctx, a.ChatJID, a.PollMessageID)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return mcp.NewToolResultError("poll: not found in cache (wait for message sync)"), nil
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
		mcp.WithDescription("Send a contact card. When `vcard` is omitted, a minimal vCard 3.0 is synthesised from `name` + `phone`."),
		mcp.WithString("recipient", mcp.Required(), mcp.Description(recipientDesc)),
		mcp.WithString("name", mcp.Required(), mcp.Description("contact display name (also used for the synthesised vCard)")),
		mcp.WithString("phone", mcp.Description("phone number (digits preferred); used to synthesise the vCard when `vcard` is not supplied")),
		mcp.WithString("vcard", mcp.Description("raw vCard 3.0 string; when set, name+phone synthesis is skipped")),
		mcp.WithDestructiveHintAnnotation(false),
	)
	s.mcp.AddTool(tool, mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, a sendContactCardArgs) (*mcp.CallToolResult, error) {
		if r := requireNonEmpty("recipient", a.Recipient); r != nil {
			return r, nil
		}
		if a.Name == "" && a.VCard == "" {
			return mcp.NewToolResultError("name: required when vcard is not provided"), nil
		}
		r := s.client.SendContactCard(ctx, a.Recipient, a.Name, a.Phone, a.VCard)
		return resultJSON(r)
	}))
}
