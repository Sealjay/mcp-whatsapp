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
		mcp.WithDescription("Send a new WhatsApp poll message with 2 to 32 options; recipients see a votable poll card and an outgoing row plus poll metadata is persisted locally so votes can be tallied. Reversible via delete_message (revoke). Use send_poll_vote to cast votes and get_poll_results to read tallies. Returns a JSON object `{Success, Message, ID}` where `ID` is the poll message ID on success."),
		mcp.WithString("recipient", mcp.Required(), mcp.Description(recipientDesc)),
		mcp.WithString("question", mcp.Required(), mcp.Description("poll question text shown above the options")),
		mcp.WithArray("options", mcp.Required(), mcp.Items(map[string]any{"type": "string"}), mcp.Description("poll option labels; must contain between 2 and 32 entries")),
		mcp.WithNumber("selectable_count", mcp.DefaultNumber(1), mcp.Description("how many options each voter may pick; 1 = single-choice (default), higher = multi-select up to this cap")),
		mcp.WithReadOnlyHintAnnotation(false),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(false),
		mcp.WithOpenWorldHintAnnotation(true),
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
		mcp.WithDescription("Cast or re-cast a vote on a previously-seen poll; each call replaces the caller's prior vote on that poll, and the new tally is broadcast to the chat. Reversible by calling again with the desired option set (or an empty list to clear). Prerequisite: the poll must be in the local cache, i.e. send_poll was used or we received the poll via sync. Returns a JSON object `{Success, Message, ID}` where `ID` is the vote message ID."),
		mcp.WithString("chat_jid", mcp.Required(), mcp.Description(jidDesc)),
		mcp.WithString("poll_message_id", mcp.Required(), mcp.Description("WhatsApp message ID of the poll to vote on (use `ID` returned by send_poll, or `message_id` from list_messages)")),
		mcp.WithArray("options", mcp.Required(), mcp.Items(map[string]any{"type": "string"}), mcp.Description("option labels to pick; must match the poll's option text exactly, between 1 and 32 entries")),
		mcp.WithReadOnlyHintAnnotation(false),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(true),
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
		mcp.WithDescription(offlineSafePrefix+"Return the current vote tally for a cached poll. Read-only; no side effects. Zero-vote options are included so the response always lists every original option. Returns a JSON object `{poll_message_id, chat_jid, tally}` where `tally` is `option_label -> vote_count`."),
		mcp.WithString("chat_jid", mcp.Required(), mcp.Description(jidDesc)),
		mcp.WithString("poll_message_id", mcp.Required(), mcp.Description("WhatsApp message ID of the poll to tally (use `ID` from send_poll, or `message_id` from list_messages)")),
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
		mcp.WithDescription("Send a WhatsApp contact card; recipients see a tappable contact entry they can save to their address book and the outgoing message is persisted to the local cache. When `vcard` is omitted a minimal vCard 3.0 is synthesised from `name` + `phone`. Reversible via delete_message (revoke). Returns a JSON object `{Success, Message, ID}` where `ID` is the WhatsApp message ID on success."),
		mcp.WithString("recipient", mcp.Required(), mcp.Description(recipientDesc)),
		mcp.WithString("name", mcp.Required(), mcp.Description("contact display name; also used as the FN in the synthesised vCard")),
		mcp.WithString("phone", mcp.Description("phone number (digits preferred); embedded in the synthesised vCard when `vcard` is not supplied")),
		mcp.WithString("vcard", mcp.Description("raw vCard 3.0 string; when set, name+phone synthesis is skipped and this string is sent as-is")),
		mcp.WithReadOnlyHintAnnotation(false),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(false),
		mcp.WithOpenWorldHintAnnotation(true),
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
