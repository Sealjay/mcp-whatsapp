package client

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/appstate"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"

	"github.com/sealjay/mcp-whatsapp/internal/store"
	"github.com/sealjay/mcp-whatsapp/internal/transcribe"
)

var deleteMsgTS = time.Date(2026, 3, 1, 9, 30, 0, 0, time.UTC)

// newDeleteForMeClient returns an edit test client (Bob's LID mapped) that
// looks connected and records app-state patches instead of sending them.
// sendErr, when set, is returned by the send after recording the patch.
func newDeleteForMeClient(t *testing.T) (*Client, *store.Store, *[]appstate.PatchInfo, *error) {
	t.Helper()
	c, s := newEditTestClient(t)
	// Bob's chat can also be cached under his LID (before the mapping).
	if err := s.StoreChat(bobLID.String(), "Bob", liveTS); err != nil {
		t.Fatalf("seed chat: %v", err)
	}
	c.isConnected = func() bool { return true }
	sent := &[]appstate.PatchInfo{}
	var sendErr error
	c.sendAppState = func(_ context.Context, p appstate.PatchInfo) error {
		*sent = append(*sent, p)
		return sendErr
	}
	// Group-info lookups never reach the network: the group is
	// phone-number addressed, with Bob and Carol as members, unless a test
	// says otherwise.
	withGroup(c, types.AddressingModePN, pnMember(bobJID, bobLID), pnMember(carolJID, carolLID))
	return c, s, sent, &sendErr
}

var carolLID = types.NewJID("11112222", types.HiddenUserServer)

// withGroup answers group-info lookups with the given addressing mode and
// participants.
func withGroup(c *Client, mode types.AddressingMode, members ...types.GroupParticipant) {
	c.groupInfo = func(_ context.Context, jid types.JID) (*types.GroupInfo, error) {
		return &types.GroupInfo{JID: jid, AddressingMode: mode, Participants: members}, nil
	}
}

// pnMember and lidMember describe a participant as whatsmeow reports it in
// a phone-number-addressed and a LID-addressed group.
func pnMember(pn, lid types.JID) types.GroupParticipant {
	return types.GroupParticipant{JID: pn, PhoneNumber: pn, LID: lid}
}

func lidMember(pn, lid types.JID) types.GroupParticipant {
	return types.GroupParticipant{JID: lid, PhoneNumber: pn, LID: lid}
}

func cacheText(t *testing.T, s *store.Store, chat types.JID, id, sender string, fromMe bool) {
	t.Helper()
	if err := s.StoreMessage(context.Background(), store.Message{
		ID: id, ChatJID: chat.String(), Sender: sender, Content: "body of " + id,
		Timestamp: deleteMsgTS, IsFromMe: fromMe,
	}, nil, nil, nil, 0); err != nil {
		t.Fatalf("cache %s: %v", id, err)
	}
}

func hiddenForMe(t *testing.T, s *store.Store, chat types.JID, id string) bool {
	t.Helper()
	hidden, err := s.IsDeletedForMe(context.Background(), id, chat.String())
	if err != nil {
		t.Fatalf("IsDeletedForMe: %v", err)
	}
	return hidden
}

func TestDeleteForMePatch_Layout(t *testing.T) {
	p := deleteForMePatch("123456789@g.us", "MSG-1", false, "447700000002@s.whatsapp.net", deleteMsgTS)
	if p.Type != appstate.WAPatchRegularHigh || len(p.Mutations) != 1 {
		t.Fatalf("patch = %+v, want one regular_high mutation", p)
	}
	m := p.Mutations[0]
	want := []string{appstate.IndexDeleteMessageForMe, "123456789@g.us", "MSG-1", "0", "447700000002@s.whatsapp.net"}
	if strings.Join(m.Index, "|") != strings.Join(want, "|") {
		t.Errorf("index = %q, want %q", m.Index, want)
	}
	if m.Version != 3 {
		t.Errorf("version = %d, want 3", m.Version)
	}
	act := m.Value.GetDeleteMessageForMeAction()
	if act == nil || act.DeleteMedia == nil || act.GetDeleteMedia() || act.GetMessageTimestamp() != deleteMsgTS.Unix() {
		t.Errorf("action = %+v, want deleteMedia false and the message timestamp in seconds", act)
	}

	own := deleteForMePatch("447700000001@s.whatsapp.net", "MSG-2", true, "", deleteMsgTS)
	if got := own.Mutations[0].Index; got[3] != "1" || got[4] != "0" {
		t.Errorf("own direct message index = %q, want from-me 1 and participant 0", got)
	}
}

// Each case checks the exact index passed to sendAppState: the chat address
// (group JID; LID for a direct chat when known, else the phone number), the
// from-me flag, the participant, and the cached timestamp.
func TestDeleteForMe_Addressing(t *testing.T) {
	cases := []struct {
		name      string
		setup     func(t *testing.T, c *Client, s *store.Store)
		chatArg   string
		id        string
		wantIndex []string
	}{
		{
			name:      "direct chat without a known LID uses the phone number",
			setup:     func(t *testing.T, _ *Client, s *store.Store) { cacheText(t, s, aliceJID, "A-1", aliceJID.User, false) },
			chatArg:   aliceJID.String(),
			id:        "A-1",
			wantIndex: []string{aliceJID.String(), "A-1", "0", "0"},
		},
		{
			name:      "bare phone digits are accepted",
			setup:     func(t *testing.T, _ *Client, s *store.Store) { cacheText(t, s, aliceJID, "A-2", "", true) },
			chatArg:   aliceJID.User,
			id:        "A-2",
			wantIndex: []string{aliceJID.String(), "A-2", "1", "0"},
		},
		{
			name:      "direct chat with a known LID uses the LID",
			setup:     func(t *testing.T, _ *Client, s *store.Store) { cacheText(t, s, bobJID, "B-1", bobJID.User, false) },
			chatArg:   bobJID.String(),
			id:        "B-1",
			wantIndex: []string{bobLID.String(), "B-1", "0", "0"},
		},
		{
			name:      "a LID chat argument finds the row cached under the phone number",
			setup:     func(t *testing.T, _ *Client, s *store.Store) { cacheText(t, s, bobJID, "B-2", "", true) },
			chatArg:   bobLID.String(),
			id:        "B-2",
			wantIndex: []string{bobLID.String(), "B-2", "1", "0"},
		},
		{
			name:      "own group message names no participant",
			setup:     func(t *testing.T, _ *Client, s *store.Store) { cacheText(t, s, groupJID, "G-1", historyOwnPhone, true) },
			chatArg:   groupJID.String(),
			id:        "G-1",
			wantIndex: []string{groupJID.String(), "G-1", "1", "0"},
		},
		{
			name:      "member's group message names the sender",
			setup:     func(t *testing.T, _ *Client, s *store.Store) { cacheText(t, s, groupJID, "G-2", carolJID.User, false) },
			chatArg:   groupJID.String(),
			id:        "G-2",
			wantIndex: []string{groupJID.String(), "G-2", "0", carolJID.String()},
		},
		{
			name: "a LID sender is named by phone number in a phone-number-addressed group",
			setup: func(t *testing.T, _ *Client, s *store.Store) {
				cacheText(t, s, groupJID, "G-4", bobLID.String(), false)
			},
			chatArg:   groupJID.String(),
			id:        "G-4",
			wantIndex: []string{groupJID.String(), "G-4", "0", bobJID.String()},
		},
		{
			name: "member's message in a LID-addressed group names the sender's LID",
			setup: func(t *testing.T, c *Client, s *store.Store) {
				cacheText(t, s, groupJID, "G-3", carolJID.User, false)
				withGroup(c, types.AddressingModeLID, lidMember(carolJID, carolLID))
			},
			chatArg:   groupJID.String(),
			id:        "G-3",
			wantIndex: []string{groupJID.String(), "G-3", "0", carolLID.String()},
		},
		{
			name: "a former member is named through a known PN/LID mapping",
			setup: func(t *testing.T, c *Client, s *store.Store) {
				cacheText(t, s, groupJID, "G-5", bobJID.User, false)
				withGroup(c, types.AddressingModeLID, lidMember(carolJID, carolLID))
			},
			chatArg:   groupJID.String(),
			id:        "G-5",
			wantIndex: []string{groupJID.String(), "G-5", "0", bobLID.String()},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, s, sent, _ := newDeleteForMeClient(t)
			tc.setup(t, c, s)
			r := c.DeleteMessageWithScope(context.Background(), tc.chatArg, tc.id, "", DeleteForMe)
			if r.Outcome != DeleteSent {
				t.Fatalf("result = %+v, want sent", r)
			}
			if len(*sent) != 1 {
				t.Fatalf("sent %d patches, want 1", len(*sent))
			}
			m := (*sent)[0].Mutations[0]
			want := append([]string{appstate.IndexDeleteMessageForMe}, tc.wantIndex...)
			if strings.Join(m.Index, "|") != strings.Join(want, "|") {
				t.Errorf("index = %q, want %q", m.Index, want)
			}
			if got := m.Value.GetDeleteMessageForMeAction().GetMessageTimestamp(); got != deleteMsgTS.Unix() {
				t.Errorf("message timestamp = %d, want the cached %d", got, deleteMsgTS.Unix())
			}
		})
	}
}

func TestDeleteForMe_SentHidesTheCachedMessage(t *testing.T) {
	c, s, _, _ := newDeleteForMeClient(t)
	cacheText(t, s, bobJID, "B-1", bobJID.User, false)

	r := c.DeleteMessageWithScope(context.Background(), bobJID.String(), "B-1", "", DeleteForMe)
	if r.Outcome != DeleteSent || r.Scope != DeleteForMe || r.Reversible {
		t.Fatalf("result = %+v, want a sent, irreversible delete for me", r)
	}
	if strings.Contains(r.Message, "applied") {
		t.Errorf("message %q claims the phone applied the deletion", r.Message)
	}
	if !hiddenForMe(t, s, bobJID, "B-1") {
		t.Fatal("accepted delete for me left the message visible")
	}
	// A copy that later arrives under the LID stays hidden.
	cacheText(t, s, bobLID, "B-1", bobLID.User, false)
	if !hiddenForMe(t, s, bobLID, "B-1") {
		t.Error("LID copy of a message deleted for me is visible")
	}
}

func TestDeleteForMe_UnknownOutcomeLeavesTheCache(t *testing.T) {
	c, s, sent, sendErr := newDeleteForMeClient(t)
	cacheText(t, s, aliceJID, "A-1", aliceJID.User, false)
	*sendErr = errors.New("failed to fetch app state after sending update: timed out")

	r := c.DeleteMessageWithScope(context.Background(), aliceJID.String(), "A-1", "", DeleteForMe)
	if r.Outcome != DeleteUnknown {
		t.Fatalf("result = %+v, want unknown", r)
	}
	if !strings.Contains(r.Message, "not retried") || !strings.Contains(r.Message, "may or may not") {
		t.Errorf("message %q should say the outcome is unknown and was not retried", r.Message)
	}
	if len(*sent) != 1 {
		t.Errorf("sent %d patches, want exactly 1 (no retry)", len(*sent))
	}
	if hiddenForMe(t, s, aliceJID, "A-1") {
		t.Error("an unconfirmed delete for me changed the cache")
	}
	// Nor was a marker left for a later copy to pick up.
	cacheText(t, s, aliceJID, "A-1", aliceJID.User, false)
	if hiddenForMe(t, s, aliceJID, "A-1") {
		t.Error("an unconfirmed delete for me left a deletion marker")
	}
}

func TestDeleteMessageWithScope_Refusals(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T, c *Client, s *store.Store)
		chat  string
		id    string
		scope string
		want  string
	}{
		{name: "unknown scope", chat: aliceJID.String(), id: "A-1", scope: "for_them", want: "scope: must be"},
		{name: "missing chat", chat: "", id: "A-1", scope: DeleteForMe, want: "chat_jid: required"},
		{name: "missing message ID", chat: aliceJID.String(), id: " ", scope: DeleteForMe, want: "message_id: required"},
		{name: "invalid chat", chat: "+44 7700", id: "A-1", scope: DeleteForMe, want: "chat_jid:"},
		{name: "not a chat", chat: "status@broadcast", id: "A-1", scope: DeleteForMe, want: "not a direct or group chat"},
		{name: "not cached", chat: aliceJID.String(), id: "NOPE", scope: DeleteForMe, want: "not in the local cache"},
		{
			name: "not connected",
			setup: func(t *testing.T, c *Client, s *store.Store) {
				cacheText(t, s, aliceJID, "A-1", aliceJID.User, false)
				c.isConnected = func() bool { return false }
			},
			chat: aliceJID.String(), id: "A-1", scope: DeleteForMe, want: "not connected",
		},
		{
			name: "alias rows that disagree",
			setup: func(t *testing.T, c *Client, s *store.Store) {
				cacheText(t, s, bobJID, "B-1", bobJID.User, false)
				if err := s.StoreMessage(context.Background(), store.Message{
					ID: "B-1", ChatJID: bobLID.String(), Sender: "", Content: "x", Timestamp: deleteMsgTS.Add(time.Minute), IsFromMe: true,
				}, nil, nil, nil, 0); err != nil {
					t.Fatal(err)
				}
			},
			chat: bobJID.String(), id: "B-1", scope: DeleteForMe, want: "ambiguous",
		},
		{
			name:  "group message with no cached sender",
			setup: func(t *testing.T, c *Client, s *store.Store) { cacheText(t, s, groupJID, "G-1", "", false) },
			chat:  groupJID.String(), id: "G-1", scope: DeleteForMe, want: "sender",
		},
		{
			name: "group info unavailable for a member's message",
			setup: func(t *testing.T, c *Client, s *store.Store) {
				cacheText(t, s, groupJID, "G-1", carolJID.User, false)
				c.groupInfo = func(context.Context, types.JID) (*types.GroupInfo, error) {
					return nil, errors.New("timed out")
				}
			},
			chat: groupJID.String(), id: "G-1", scope: DeleteForMe, want: "addressing mode",
		},
		{
			name: "no LID known for a member of a LID-addressed group",
			setup: func(t *testing.T, c *Client, s *store.Store) {
				cacheText(t, s, groupJID, "G-1", carolJID.User, false)
				withGroup(c, types.AddressingModeLID, types.GroupParticipant{JID: carolJID, PhoneNumber: carolJID})
			},
			chat: groupJID.String(), id: "G-1", scope: DeleteForMe, want: "no LID is known",
		},
		{
			name: "unresolved LID sender in a phone-number-addressed group",
			setup: func(t *testing.T, c *Client, s *store.Store) {
				cacheText(t, s, groupJID, "G-1", "33334444@lid", false)
			},
			chat: groupJID.String(), id: "G-1", scope: DeleteForMe, want: "no phone number is known",
		},
		{
			name: "already deleted for me",
			setup: func(t *testing.T, c *Client, s *store.Store) {
				cacheText(t, s, aliceJID, "A-1", aliceJID.User, false)
				if _, err := s.MarkDeletedForMe(context.Background(), aliceJID.String(), "A-1", deleteMsgTS); err != nil {
					t.Fatal(err)
				}
			},
			chat: aliceJID.String(), id: "A-1", scope: DeleteForMe, want: "already deleted",
		},
		{
			// The test client's whatsmeow client is not connected, so a
			// revoke is refused before anything is sent.
			name: "revoke while disconnected",
			chat: aliceJID.String(), id: "A-1", scope: "", want: "not connected",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, s, sent, _ := newDeleteForMeClient(t)
			if tc.setup != nil {
				tc.setup(t, c, s)
			}
			r := c.DeleteMessageWithScope(context.Background(), tc.chat, tc.id, "", tc.scope)
			if r.Outcome != DeleteRefused || !strings.Contains(r.Message, tc.want) || !strings.Contains(r.Message, "Nothing was sent") {
				t.Errorf("result = %+v, want refused with %q", r, tc.want)
			}
			if len(*sent) != 0 {
				t.Errorf("sent %d patches, want none", len(*sent))
			}
		})
	}
}

// A group sender whose LID mapping was unknown at ingest is cached as a bare
// user part. Its server is never guessed: the deletion names the address a
// participant proves, or refuses and leaves the message visible.
func TestDeleteForMe_UnresolvedLIDSenderFromLiveMessage(t *testing.T) {
	unresolved := types.NewJID("33334444", types.HiddenUserServer)
	itsPN := types.NewJID("447700000004", types.DefaultUserServer)
	cases := []struct {
		name    string
		mode    types.AddressingMode
		members []types.GroupParticipant
		want    string // participant in the index; empty means refused
	}{
		{name: "not a participant and not mapped", mode: types.AddressingModePN, members: []types.GroupParticipant{pnMember(carolJID, carolLID)}},
		{name: "participant of a LID-addressed group", mode: types.AddressingModeLID, members: []types.GroupParticipant{lidMember(itsPN, unresolved)}, want: unresolved.String()},
		{name: "participant of a phone-number-addressed group", mode: types.AddressingModePN, members: []types.GroupParticipant{pnMember(itsPN, unresolved)}, want: itsPN.String()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, s, sent, _ := newDeleteForMeClient(t)
			liveMsg(c, groupJID, unresolved, false, "G-LID", deleteMsgTS, text("from an unmapped LID"))
			if r := mustCached(t, s, groupJID, "G-LID"); r.content != "from an unmapped LID" {
				t.Fatalf("cached row = %+v", r)
			}
			withGroup(c, tc.mode, tc.members...)

			r := c.DeleteMessageWithScope(context.Background(), groupJID.String(), "G-LID", "", DeleteForMe)
			if tc.want == "" {
				if r.Outcome != DeleteRefused || len(*sent) != 0 {
					t.Fatalf("result = %+v with %d patch(es) sent, want refused and nothing sent", r, len(*sent))
				}
				if hiddenForMe(t, s, groupJID, "G-LID") {
					t.Error("a refused delete for me hid the message")
				}
				return
			}
			if r.Outcome != DeleteSent || len(*sent) != 1 {
				t.Fatalf("result = %+v with %d patch(es) sent, want sent once", r, len(*sent))
			}
			if got := (*sent)[0].Mutations[0].Index[4]; got != tc.want {
				t.Errorf("participant = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestDeleteMessageWithScope_DefaultsToForEveryone(t *testing.T) {
	c, _, _, _ := newDeleteForMeClient(t)
	if r := c.DeleteMessageWithScope(context.Background(), aliceJID.String(), "A-1", "", ""); r.Scope != DeleteForEveryone {
		t.Errorf("scope = %q, want %q", r.Scope, DeleteForEveryone)
	}
}

func TestHandleDeleteForMe_HidesCachedMessage(t *testing.T) {
	c, s, _, _ := newDeleteForMeClient(t)
	cacheText(t, s, groupJID, "G-1", carolJID.User, false)

	c.handleDeleteForMe(&events.DeleteForMe{ChatJID: groupJID, SenderJID: carolJID, MessageID: "G-1", Timestamp: deleteMsgTS})
	if !hiddenForMe(t, s, groupJID, "G-1") {
		t.Fatal("a delete for me from another device left the message visible")
	}
	// Redelivery through history sync keeps it hidden.
	runHistorySync(c, groupJID.String(), historyMsg(historyKey(groupJID.String(), "G-1", false, carolJID.String()), text("body of G-1")))
	if !hiddenForMe(t, s, groupJID, "G-1") {
		t.Fatal("history sync resurrected a message deleted for me")
	}
}

func TestHandleDeleteForMe_BeforeTheMessage(t *testing.T) {
	c, s, _, _ := newDeleteForMeClient(t)

	// The deletion arrives keyed by Bob's LID before the message is cached.
	c.handleDeleteForMe(&events.DeleteForMe{ChatJID: bobLID, MessageID: "LATE-1", Timestamp: deleteMsgTS})
	liveMsg(c, bobLID, bobLID, false, "LATE-1", deleteMsgTS.Add(-time.Minute), text("arrives late"))
	if _, ok := readCached(t, s, bobJID, "LATE-1"); !ok {
		t.Fatal("late message was not cached under Bob's phone number")
	}
	if !hiddenForMe(t, s, bobJID, "LATE-1") {
		t.Fatal("a message whose deletion for me arrived first is visible")
	}
}

func TestDownload_RefusesDeletedForMe(t *testing.T) {
	c, _ := transcribeFixture(t, "audio")
	if _, err := c.store.MarkDeletedForMe(context.Background(), trJID, trMsg, deleteMsgTS); err != nil {
		t.Fatal(err)
	}
	r := c.Download(context.Background(), trMsg, trJID, "")
	if r.Success || !strings.Contains(r.Message, "deleted for me") {
		t.Errorf("Download = %+v, want a refusal naming the deletion", r)
	}
	// download_media and whatsapp://media/ resource reads use this path.
	r = c.DownloadWithMediaRetry(context.Background(), trMsg, trJID, "")
	if r.Success || !strings.Contains(r.Message, "deleted for me") {
		t.Errorf("DownloadWithMediaRetry = %+v, want a refusal naming the deletion", r)
	}
}

func TestTranscribeAudio_RefusesDeletedForMe(t *testing.T) {
	c, calls := transcribeFixture(t, "audio")
	ctx := context.Background()
	if _, _, err := c.TranscribeAudio(ctx, trMsg, trJID, "es"); err != nil {
		t.Fatalf("first transcription: %v", err)
	}
	if _, err := c.store.MarkDeletedForMe(ctx, trJID, trMsg, deleteMsgTS); err != nil {
		t.Fatal(err)
	}
	// The cached transcript is not returned either.
	if _, _, err := c.TranscribeAudio(ctx, trMsg, trJID, "es"); !errors.Is(err, store.ErrDeletedForMe) {
		t.Errorf("TranscribeAudio after the deletion err = %v, want ErrDeletedForMe", err)
	}
	if *calls != 1 {
		t.Errorf("the service was called %d times, want 1", *calls)
	}
}

func TestTranscribeAudio_DeletedForMeDuringTranscription(t *testing.T) {
	c, _ := transcribeFixture(t, "audio")
	ctx := context.Background()
	c.transcribeFn = func(context.Context, transcribe.Config, string, string) (transcribe.Result, error) {
		if _, err := c.store.MarkDeletedForMe(ctx, trJID, trMsg, deleteMsgTS); err != nil {
			t.Error(err)
		}
		return transcribe.Result{Text: "secret words"}, nil
	}
	tr, _, err := c.TranscribeAudio(ctx, trMsg, trJID, "")
	if !errors.Is(err, store.ErrDeletedForMe) || tr != nil {
		t.Fatalf("TranscribeAudio = %+v, %v; want ErrDeletedForMe and no text", tr, err)
	}
	// The transcript itself stays in the cache.
	if stored, err := c.store.GetTranscript(ctx, trMsg, trJID); err != nil || stored == nil {
		t.Errorf("stored transcript = %+v, %v; want it kept", stored, err)
	}
}
