package mcp

// Every tool is explicitly reviewed; unknown tools fail closed in read-only mode.
var readOnlyToolAccess = map[string]bool{
	"block_contact":             false,
	"create_group":              false,
	"delete_message":            false,
	"download_media":            true,
	"edit_message":              false,
	"get_blocklist":             true,
	"get_chat":                  true,
	"get_group_info":            true,
	"get_group_invite_link":     false,
	"get_message_context":       true,
	"get_poll_results":          true,
	"get_privacy_settings":      true,
	"get_status":                true,
	"is_on_whatsapp":            true,
	"join_group_with_link":      false,
	"leave_group":               false,
	"list_chats":                true,
	"list_groups":               true,
	"list_messages":             true,
	"mark_chat_read":            false,
	"mark_read":                 false,
	"pairing_status":            true,
	"request_sync":              true,
	"search_contacts":           true,
	"send_audio_message":        false,
	"send_contact_card":         false,
	"send_file":                 false,
	"send_message":              false,
	"send_poll":                 false,
	"send_poll_vote":            false,
	"send_presence":             false,
	"send_reaction":             false,
	"send_reply":                false,
	"send_typing":               false,
	"set_group_announce":        false,
	"set_group_locked":          false,
	"set_group_name":            false,
	"set_group_topic":           false,
	"set_privacy_setting":       false,
	"set_status_message":        false,
	"unblock_contact":           false,
	"update_group_participants": false,
}

// registerTools wires every MCP tool to the bound WhatsApp client.
//
// Tool registrations are split by domain:
//   - tools_query.go   — read-only data retrieval (list_chats, search_contacts, …)
//   - tools_send.go    — outbound messages (send_message, send_file, send_reaction, …)
//   - tools_message.go — message mutation & history (edit, delete, mark_read, sync, …)
//   - tools_groups.go  — group management (create, leave, participants, settings, …)
//   - tools_media.go   — polls + contact cards
//   - tools_privacy.go — blocklist, presence, privacy settings, status message
//
// Shared helpers live in tools_core.go (parseTimestamp, normalizeRecipientToChatJID,
// maybeMarkChatRead). Common result helpers live in result.go.
func (s *Server) registerTools() {
	s.registerQueryTools()
	s.registerSendTools()
	s.registerMessageTools()

	// Phase 1 — group management + blocklist.
	s.registerGroupTools()
	s.registerPrivacyTools()

	// Phase 2 — polls + contact cards.
	s.registerMediaTools()
}
