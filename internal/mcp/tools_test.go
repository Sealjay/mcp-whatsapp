package mcp

import (
	"sort"
	"testing"
)

// expectedToolNames is the full set of MCP tools this server must register.
// Keeping it as a sorted slice makes diff output easy to read when registration
// drifts.
var expectedToolNames = []string{
	"block_contact",
	"create_group",
	"delete_message",
	"download_media",
	"edit_message",
	"get_blocklist",
	"get_chat",
	"get_group_info",
	"get_group_invite_link",
	"get_message_context",
	"get_poll_results",
	"get_privacy_settings",
	"get_status",
	"is_on_whatsapp",
	"join_group_with_link",
	"leave_group",
	"list_chats",
	"list_groups",
	"list_messages",
	"mark_chat_read",
	"mark_read",
	"request_sync",
	"search_contacts",
	"send_audio_message",
	"send_contact_card",
	"send_file",
	"send_message",
	"send_poll",
	"send_poll_vote",
	"send_presence",
	"send_reaction",
	"send_reply",
	"send_typing",
	"set_group_announce",
	"set_group_locked",
	"set_group_name",
	"set_group_topic",
	"set_privacy_setting",
	"set_status_message",
	"unblock_contact",
	"update_group_participants",
}

func TestNewServer_RegistersAllTools(t *testing.T) {
	// The client is only used inside handler closures — none of those run at
	// registration time. Passing nil keeps the test hermetic (no whatsmeow,
	// no sqlite, no network).
	s := NewServer(nil)
	if s == nil {
		t.Fatal("NewServer returned nil")
	}
	if s.MCP() == nil {
		t.Fatal("Server.MCP() returned nil")
	}

	got := s.MCP().ListTools()
	if len(got) != len(expectedToolNames) {
		names := make([]string, 0, len(got))
		for name := range got {
			names = append(names, name)
		}
		sort.Strings(names)
		t.Fatalf("registered %d tools, want %d. Got: %v\nWanted: %v",
			len(got), len(expectedToolNames), names, expectedToolNames)
	}

	for _, want := range expectedToolNames {
		if tool := s.MCP().GetTool(want); tool == nil {
			t.Errorf("tool %q not registered", want)
			continue
		}
	}

	// Every registered tool must also have a non-nil handler.
	for name, tool := range got {
		if tool == nil {
			t.Errorf("tool %q: ServerTool is nil", name)
			continue
		}
		if tool.Handler == nil {
			t.Errorf("tool %q: Handler is nil", name)
		}
		if tool.Tool.Name != name {
			t.Errorf("tool %q: Tool.Name = %q (mismatched key vs name)", name, tool.Tool.Name)
		}
	}

	// No extra tools slipped in.
	extras := []string{}
	wanted := make(map[string]struct{}, len(expectedToolNames))
	for _, n := range expectedToolNames {
		wanted[n] = struct{}{}
	}
	for name := range got {
		if _, ok := wanted[name]; !ok {
			extras = append(extras, name)
		}
	}
	if len(extras) > 0 {
		sort.Strings(extras)
		t.Errorf("unexpected tools registered: %v", extras)
	}
}

func TestNewServer_ToolCount(t *testing.T) {
	s := NewServer(nil)
	const want = 41
	if got := len(s.MCP().ListTools()); got != want {
		t.Errorf("tool count = %d, want %d", got, want)
	}
}

// TestToolsByDomain verifies that at least one tool from each domain file is
// registered. This catches accidental omissions when the registerXxxTools()
// aggregator in a domain file is not wired from registerTools().
//
// Heuristic: we pick one representative tool name per source file. The mapping
// is maintained manually — if a domain file is added or a tool is renamed the
// test must be updated.
func TestToolsByDomain(t *testing.T) {
	s := NewServer(nil)
	got := s.MCP().ListTools()

	// domain file → representative tool name(s) that MUST be present.
	domains := map[string][]string{
		"tools_query.go":   {"list_chats", "search_contacts"},
		"tools_send.go":    {"send_message", "send_file"},
		"tools_message.go": {"edit_message", "mark_read"},
		"tools_groups.go":  {"create_group", "leave_group"},
		"tools_media.go":   {"send_poll", "send_contact_card"},
		"tools_privacy.go": {"get_blocklist", "send_presence"},
	}

	for file, names := range domains {
		for _, name := range names {
			if _, ok := got[name]; !ok {
				t.Errorf("domain %s: expected tool %q not registered", file, name)
			}
		}
	}
}
