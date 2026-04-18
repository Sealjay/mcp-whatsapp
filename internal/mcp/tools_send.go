package mcp

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/sealjay/mcp-whatsapp/internal/client"
	"github.com/sealjay/mcp-whatsapp/internal/media"
)

// registerSendTools wires outbound-message tools: send_message, send_file,
// send_audio_message, send_reaction, send_reply, send_contact_card,
// send_typing.
func (s *Server) registerSendTools() {
	s.registerSendMessage()
	s.registerSendFile()
	s.registerSendAudioMessage()
	s.registerSendReaction()
	s.registerSendReply()
	s.registerSendTyping()
}

// -- send_message -----------------------------------------------------------

type sendMessageArgs struct {
	Recipient    string `json:"recipient"`
	Message      string `json:"message"`
	MarkChatRead bool   `json:"mark_chat_read,omitempty"`
}

func (s *Server) registerSendMessage() {
	tool := mcp.NewTool("send_message",
		mcp.WithDescription("Send a WhatsApp message to a person (phone number or JID) or group (JID)."),
		mcp.WithString("recipient", mcp.Required(), mcp.Description(recipientDesc)),
		mcp.WithString("message", mcp.Required(), mcp.Description("message body")),
		mcp.WithBoolean("mark_chat_read", mcp.DefaultBool(false), mcp.Description("On successful send, ack recent incoming messages so the phone drops the unread badge.")),
	)
	s.mcp.AddTool(tool, mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, a sendMessageArgs) (*mcp.CallToolResult, error) {
		if a.Recipient == "" {
			return mcp.NewToolResultError("recipient must be provided"), nil
		}
		r := s.client.Send(ctx, a.Recipient, a.Message)
		s.maybeMarkChatRead(ctx, r, a.Recipient, a.MarkChatRead)
		return resultJSON(r)
	}))
}

// -- send_file --------------------------------------------------------------

type sendFileArgs struct {
	Recipient    string `json:"recipient"`
	MediaPath    string `json:"media_path"`
	Caption      string `json:"caption,omitempty"`
	MarkChatRead bool   `json:"mark_chat_read,omitempty"`
	ViewOnce     bool   `json:"view_once,omitempty"`
}

func (s *Server) registerSendFile() {
	tool := mcp.NewTool("send_file",
		mcp.WithDescription("Send a picture, video, document, or raw audio via WhatsApp."),
		mcp.WithString("recipient", mcp.Required(), mcp.Description(recipientDesc)),
		mcp.WithString("media_path", mcp.Required(), mcp.Description("absolute path to the media file (must sit under the configured media root)")),
		mcp.WithString("caption", mcp.Description("Optional caption for image/video/document")),
		mcp.WithBoolean("mark_chat_read", mcp.DefaultBool(false), mcp.Description("On successful send, ack recent incoming messages so the phone drops the unread badge.")),
		mcp.WithBoolean("view_once", mcp.DefaultBool(false), mcp.Description("If true, mark image/video/audio submessages as view-once. Silently ignored for documents.")),
	)
	s.mcp.AddTool(tool, mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, a sendFileArgs) (*mcp.CallToolResult, error) {
		if a.Recipient == "" || a.MediaPath == "" {
			return mcp.NewToolResultError("recipient and media_path are required"), nil
		}
		safePath, err := s.client.ValidateMediaPath(a.MediaPath)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		r := s.client.SendMediaWithOptions(ctx, client.SendMediaOptions{
			Recipient: a.Recipient,
			Caption:   a.Caption,
			MediaPath: safePath,
			ViewOnce:  a.ViewOnce,
		})
		s.maybeMarkChatRead(ctx, r, a.Recipient, a.MarkChatRead)
		return resultJSON(r)
	}))
}

// -- send_audio_message -----------------------------------------------------

type sendAudioArgs struct {
	Recipient    string `json:"recipient"`
	MediaPath    string `json:"media_path"`
	MarkChatRead bool   `json:"mark_chat_read,omitempty"`
	ViewOnce     bool   `json:"view_once,omitempty"`
}

func (s *Server) registerSendAudioMessage() {
	tool := mcp.NewTool("send_audio_message",
		mcp.WithDescription("Send any audio file as a WhatsApp voice note. Non-ogg inputs are converted via ffmpeg."),
		mcp.WithString("recipient", mcp.Required(), mcp.Description(recipientDesc)),
		mcp.WithString("media_path", mcp.Required(), mcp.Description("absolute path to the media file (must sit under the configured media root)")),
		mcp.WithBoolean("mark_chat_read", mcp.DefaultBool(false), mcp.Description("On successful send, ack recent incoming messages so the phone drops the unread badge.")),
		mcp.WithBoolean("view_once", mcp.DefaultBool(false), mcp.Description("If true, mark the voice note as view-once.")),
	)
	s.mcp.AddTool(tool, mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, a sendAudioArgs) (*mcp.CallToolResult, error) {
		if a.Recipient == "" || a.MediaPath == "" {
			return mcp.NewToolResultError("recipient and media_path are required"), nil
		}
		safePath, err := s.client.ValidateMediaPath(a.MediaPath)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		path := safePath
		if !strings.HasSuffix(strings.ToLower(path), ".ogg") {
			converted, err := media.ConvertToOpusOgg(ctx, path)
			if err != nil {
				return mcp.NewToolResultError(fmt.Sprintf("audio conversion failed: %v (install ffmpeg, or call send_file for raw audio)", err)), nil
			}
			defer os.Remove(converted)
			path = converted
		}
		r := s.client.SendMediaWithOptions(ctx, client.SendMediaOptions{
			Recipient: a.Recipient,
			MediaPath: path,
			ViewOnce:  a.ViewOnce,
		})
		s.maybeMarkChatRead(ctx, r, a.Recipient, a.MarkChatRead)
		return resultJSON(r)
	}))
}

// -- send_reaction ----------------------------------------------------------

type sendReactionArgs struct {
	ChatJID   string `json:"chat_jid"`
	MessageID string `json:"message_id"`
	SenderJID string `json:"sender_jid,omitempty"`
	Emoji     string `json:"emoji"`
}

func (s *Server) registerSendReaction() {
	tool := mcp.NewTool("send_reaction",
		mcp.WithDescription("React to a message. Empty emoji clears an existing reaction."),
		mcp.WithString("chat_jid", mcp.Required(), mcp.Description(jidDesc)),
		mcp.WithString("message_id", mcp.Required(), mcp.Description("WhatsApp message ID")),
		mcp.WithString("sender_jid", mcp.Description("original sender; required in group chats ("+jidDesc+")")),
		mcp.WithString("emoji", mcp.Description("single emoji, or empty string to clear the reaction")),
	)
	s.mcp.AddTool(tool, mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, a sendReactionArgs) (*mcp.CallToolResult, error) {
		if err := s.client.SendReaction(ctx, a.ChatJID, a.MessageID, a.SenderJID, a.Emoji); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return mcp.NewToolResultText("Reaction sent"), nil
	}))
}

// -- send_reply -------------------------------------------------------------

type sendReplyArgs struct {
	ChatJID         string `json:"chat_jid"`
	TargetMessageID string `json:"target_message_id"`
	TargetSenderJID string `json:"target_sender_jid,omitempty"`
	Body            string `json:"body"`
}

func (s *Server) registerSendReply() {
	tool := mcp.NewTool("send_reply",
		mcp.WithDescription("Send a text reply that quotes a previous message."),
		mcp.WithString("chat_jid", mcp.Required(), mcp.Description(jidDesc)),
		mcp.WithString("target_message_id", mcp.Required(), mcp.Description("WhatsApp message ID")),
		mcp.WithString("target_sender_jid", mcp.Description("original sender; required in group chats ("+jidDesc+")")),
		mcp.WithString("body", mcp.Required(), mcp.Description("reply text")),
	)
	s.mcp.AddTool(tool, mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, a sendReplyArgs) (*mcp.CallToolResult, error) {
		if err := s.client.SendReply(ctx, a.ChatJID, a.TargetMessageID, a.TargetSenderJID, a.Body); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return mcp.NewToolResultText("Reply sent"), nil
	}))
}

// -- send_typing ------------------------------------------------------------

type sendTypingArgs struct {
	ChatJID string `json:"chat_jid"`
	Active  bool   `json:"active"`
	Kind    string `json:"kind,omitempty"`
}

func (s *Server) registerSendTyping() {
	tool := mcp.NewTool("send_typing",
		mcp.WithDescription("Set typing/recording presence for a chat."),
		mcp.WithString("chat_jid", mcp.Required(), mcp.Description(jidDesc)),
		mcp.WithBoolean("active", mcp.Required(), mcp.Description("True = composing/recording, false = paused")),
		mcp.WithString("kind", mcp.Description("'' for text (default) or 'audio' for recording")),
	)
	s.mcp.AddTool(tool, mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, a sendTypingArgs) (*mcp.CallToolResult, error) {
		if err := s.client.SendTyping(ctx, a.ChatJID, a.Active, a.Kind); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		state := "paused"
		if a.Active {
			state = "active"
		}
		return mcp.NewToolResultText(fmt.Sprintf("Presence %s for %s", state, a.ChatJID)), nil
	}))
}
