package client

import (
	"context"
	"strings"
	"testing"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"
)

// newDisconnectedClient returns a Client whose wa field is nil. That makes
// (*whatsmeow.Client).IsConnected return false — which all feature methods
// gate on — so we can exercise the "not-connected" error branch without
// dragging in sqlite, sockets, or upstream mocks.
func newDisconnectedClient() *Client {
	return &Client{log: NewStderrLogger("test", "ERROR", false), isConnected: func() bool { return false }}
}

func TestParseParticipantJID_PhoneOnly(t *testing.T) {
	got, err := parseRecipient("447700000001")
	if err != nil {
		t.Fatalf("parseRecipient(phone) error: %v", err)
	}
	if got.User != "447700000001" || got.Server != types.DefaultUserServer {
		t.Fatalf("parseRecipient(phone) = %v, want %s@%s", got, "447700000001", types.DefaultUserServer)
	}
}

func TestParseParticipantJID_FullJID(t *testing.T) {
	got, err := parseRecipient("447700000001@s.whatsapp.net")
	if err != nil {
		t.Fatalf("parseRecipient(jid) error: %v", err)
	}
	if got.User != "447700000001" {
		t.Fatalf("parseRecipient(jid).User = %q, want 447700000001", got.User)
	}
}

func TestParseParticipantJID_Empty(t *testing.T) {
	if _, err := parseRecipient(""); err == nil {
		t.Fatalf("parseRecipient(\"\") expected error")
	}
	if _, err := parseRecipient("   "); err == nil {
		t.Fatalf("parseRecipient(whitespace) expected error")
	}
}

func TestParseParticipantJID_LeadingPlus(t *testing.T) {
	got, err := parseRecipient("+447967960994")
	if err != nil {
		t.Fatalf("parseRecipient(leading plus) error: %v", err)
	}
	if got.User != "447967960994" {
		t.Fatalf("parseRecipient(leading plus).User = %q, want %q", got.User, "447967960994")
	}
}

func TestParseParticipantJID_NonDigit(t *testing.T) {
	if _, err := parseRecipient("44 7967 960994"); err == nil {
		t.Fatalf("parseRecipient(\"44 7967 960994\") expected error")
	}
	if _, err := parseRecipient("hello"); err == nil {
		t.Fatalf("parseRecipient(\"hello\") expected error")
	}
}

// NOTE: whatsmeow's types.ParseJID is intentionally lenient and does not
// reject odd inputs (it silently accepts multiple @ signs, leading @, etc.),
// so we don't try to assert on "malformed JID" rejection here — the only way
// to enforce stricter validation would be to duplicate whatsmeow's parsing.

func TestParseParticipantJIDs_Mixed(t *testing.T) {
	got, err := parseParticipantJIDs([]string{"447700000001", "447700000002@s.whatsapp.net"})
	if err != nil {
		t.Fatalf("parseParticipantJIDs error: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("parseParticipantJIDs len = %d, want 2", len(got))
	}
	if got[0].User != "447700000001" || got[1].User != "447700000002" {
		t.Fatalf("parseParticipantJIDs users = [%q, %q]", got[0].User, got[1].User)
	}
}

func TestParseParticipantAction(t *testing.T) {
	cases := []struct {
		in   string
		want whatsmeow.ParticipantChange
	}{
		{"add", whatsmeow.ParticipantChangeAdd},
		{"ADD", whatsmeow.ParticipantChangeAdd},
		{"remove", whatsmeow.ParticipantChangeRemove},
		{"Promote", whatsmeow.ParticipantChangePromote},
		{" demote ", whatsmeow.ParticipantChangeDemote},
	}
	for _, tc := range cases {
		got, err := parseParticipantAction(tc.in)
		if err != nil {
			t.Errorf("parseParticipantAction(%q) unexpected error: %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("parseParticipantAction(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestParseParticipantAction_Invalid(t *testing.T) {
	_, err := parseParticipantAction("kick")
	if err == nil {
		t.Fatalf("parseParticipantAction(\"kick\") expected error")
	}
	msg := err.Error()
	for _, allowed := range []string{"add", "remove", "promote", "demote"} {
		if !strings.Contains(msg, allowed) {
			t.Errorf("error message %q missing allowed value %q", msg, allowed)
		}
	}
}

// The remaining tests exercise the "not connected" guard at the top of every
// feature method. When c.wa is nil, (*whatsmeow.Client).IsConnected reports
// false, so every method should bail out with that specific error before
// touching anything else.
func TestCreateGroup_NotConnected(t *testing.T) {
	c := newDisconnectedClient()
	_, _, err := c.CreateGroup(context.Background(), "ignored", []string{"447700000001"})
	assertNotConnected(t, err)
}

func TestLeaveGroup_NotConnected(t *testing.T) {
	c := newDisconnectedClient()
	err := c.LeaveGroup(context.Background(), "123@g.us")
	assertNotConnected(t, err)
}

func TestListJoinedGroups_NotConnected(t *testing.T) {
	c := newDisconnectedClient()
	_, err := c.ListJoinedGroups(context.Background())
	assertNotConnected(t, err)
}

func TestGetGroupInfoJSON_NotConnected(t *testing.T) {
	c := newDisconnectedClient()
	_, err := c.GetGroupInfoJSON(context.Background(), "123@g.us")
	assertNotConnected(t, err)
}

func TestUpdateGroupParticipants_NotConnected(t *testing.T) {
	c := newDisconnectedClient()
	_, err := c.UpdateGroupParticipants(context.Background(), "123@g.us", []string{"447700000001"}, "add", ParticipantUpdateOptions{SendInvite: true})
	assertNotConnected(t, err)
}

// Fixtures for the participant-update helpers. The group mixes a
// phone-number-addressed admin (us), a LID-addressed member, and a plain
// phone-number member.
var (
	testSelfPN   = types.NewJID("447700000001", types.DefaultUserServer)
	testSelfLID  = types.NewJID("111111111111111", types.HiddenUserServer)
	testMemberPN = types.NewJID("447700000002", types.DefaultUserServer)
	testLIDOnly  = types.NewJID("222222222222222", types.HiddenUserServer)
	testLIDPN    = types.NewJID("447700000003", types.DefaultUserServer)
	testOutsider = types.NewJID("447700000009", types.DefaultUserServer)
)

// noLID is a planParticipantChange resolver that knows no LID mappings.
func noLID(types.JID) types.JID { return types.EmptyJID }

// lidMap returns a resolver backed by a fixed phone→LID map.
func lidMap(m map[types.JID]types.JID) func(types.JID) types.JID {
	return func(pn types.JID) types.JID { return m[pn] }
}

func testGroupInfo(selfIsAdmin bool) *types.GroupInfo {
	return &types.GroupInfo{
		JID:       types.NewJID("120363000000000000", types.GroupServer),
		GroupName: types.GroupName{Name: "Test group"},
		Participants: []types.GroupParticipant{
			{JID: testSelfLID, LID: testSelfLID, PhoneNumber: testSelfPN, IsAdmin: selfIsAdmin},
			{JID: testMemberPN, PhoneNumber: testMemberPN},
			{JID: testLIDOnly, LID: testLIDOnly, PhoneNumber: testLIDPN},
		},
	}
}

func TestFindParticipant(t *testing.T) {
	info := testGroupInfo(true)
	withDevice := types.JID{User: testSelfPN.User, Server: types.DefaultUserServer, Device: 12}
	cases := []struct {
		name string
		ids  []types.JID
		want *types.GroupParticipant
	}{
		{"own PN with device", []types.JID{withDevice}, &info.Participants[0]},
		{"own LID", []types.JID{types.EmptyJID, testSelfLID}, &info.Participants[0]},
		{"PN of LID-addressed member", []types.JID{testLIDPN}, &info.Participants[2]},
		{"outsider", []types.JID{testOutsider}, nil},
		{"LID user part does not match PN server", []types.JID{types.NewJID(testSelfLID.User, types.DefaultUserServer)}, nil},
		{"empty", []types.JID{types.EmptyJID}, nil},
	}
	for _, tc := range cases {
		if got := findParticipant(info, tc.ids...); got != tc.want {
			t.Errorf("%s: findParticipant = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestCheckParticipantPermission(t *testing.T) {
	admin := testGroupInfo(true)
	member := testGroupInfo(false)
	openAdd := testGroupInfo(false)
	openAdd.MemberAddMode = types.GroupMemberAddModeAllMember

	cases := []struct {
		name    string
		info    *types.GroupInfo
		notSelf bool
		action  whatsmeow.ParticipantChange
		wantErr string
	}{
		{"admin add", admin, false, whatsmeow.ParticipantChangeAdd, ""},
		{"admin remove", admin, false, whatsmeow.ParticipantChangeRemove, ""},
		{"member add", member, false, whatsmeow.ParticipantChangeAdd, "not an admin"},
		{"member remove", member, false, whatsmeow.ParticipantChangeRemove, "not an admin"},
		{"member promote", member, false, whatsmeow.ParticipantChangePromote, "not an admin"},
		{"member add, all may add", openAdd, false, whatsmeow.ParticipantChangeAdd, ""},
		{"member remove, all may add", openAdd, false, whatsmeow.ParticipantChangeRemove, "not an admin"},
		{"not in group", admin, true, whatsmeow.ParticipantChangeAdd, "not a member"},
	}
	for _, tc := range cases {
		self := &tc.info.Participants[0]
		if tc.notSelf {
			self = nil
		}
		err := checkParticipantPermission(tc.info, self, tc.action)
		switch {
		case tc.wantErr == "" && err != nil:
			t.Errorf("%s: unexpected error %v", tc.name, err)
		case tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)):
			t.Errorf("%s: error = %v, want it to contain %q", tc.name, err, tc.wantErr)
		case tc.wantErr != "" && !strings.Contains(err.Error(), "nothing was changed"):
			t.Errorf("%s: error %q should say nothing was changed", tc.name, err)
		}
	}
}

func TestPlanParticipantChange_Add(t *testing.T) {
	info := testGroupInfo(true)
	inputs := []string{"447700000009", "447700000002", "447700000003"}
	jids, err := parseParticipantJIDs(inputs)
	if err != nil {
		t.Fatal(err)
	}
	outcomes, pending, err := planParticipantChange(info, &info.Participants[0], inputs, jids, whatsmeow.ParticipantChangeAdd, noLID)
	if err != nil {
		t.Fatalf("planParticipantChange error: %v", err)
	}
	if len(pending) != 1 || pending[0].idx != 0 || pending[0].jid != testOutsider {
		t.Fatalf("pending = %+v, want only the outsider at index 0", pending)
	}
	if outcomes[1].Status != statusAlreadyMember || outcomes[2].Status != statusAlreadyMember {
		t.Errorf("existing members = [%q, %q], want already_member", outcomes[1].Status, outcomes[2].Status)
	}
	if outcomes[2].JID != testLIDOnly.String() {
		t.Errorf("LID member JID = %q, want the group's own JID %q", outcomes[2].JID, testLIDOnly)
	}
	if outcomes[0].Input != "447700000009" || outcomes[0].Status != "" {
		t.Errorf("pending outcome = %+v, want input kept and status unset", outcomes[0])
	}
}

func TestPlanParticipantChange_RemoveUsesGroupJID(t *testing.T) {
	info := testGroupInfo(true)
	inputs := []string{"447700000003", "447700000009"}
	jids, _ := parseParticipantJIDs(inputs)
	outcomes, pending, err := planParticipantChange(info, &info.Participants[0], inputs, jids, whatsmeow.ParticipantChangeRemove, noLID)
	if err != nil {
		t.Fatalf("planParticipantChange error: %v", err)
	}
	if len(pending) != 1 || pending[0].jid != testLIDOnly {
		t.Fatalf("pending = %+v, want the member's LID %s", pending, testLIDOnly)
	}
	if outcomes[1].Status != statusNotMember {
		t.Errorf("outsider status = %q, want not_member", outcomes[1].Status)
	}
}

func TestPlanParticipantChange_Rejects(t *testing.T) {
	info := testGroupInfo(true)
	cases := []struct {
		name    string
		inputs  []string
		action  whatsmeow.ParticipantChange
		wantErr string
	}{
		{"remove self", []string{"447700000001"}, whatsmeow.ParticipantChangeRemove, "leave_group"},
		{"duplicate", []string{"447700000009", "447700000009@s.whatsapp.net"}, whatsmeow.ParticipantChangeAdd, "same person"},
		{"member as number and LID", []string{"447700000003", "222222222222222@lid"}, whatsmeow.ParticipantChangeRemove, "same person"},
	}
	for _, tc := range cases {
		jids, _ := parseParticipantJIDs(tc.inputs)
		_, _, err := planParticipantChange(info, &info.Participants[0], tc.inputs, jids, tc.action, noLID)
		if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
			t.Errorf("%s: error = %v, want it to contain %q", tc.name, err, tc.wantErr)
		}
	}
}

func TestMatchResult_LIDResponseForPNRequest(t *testing.T) {
	results := []types.GroupParticipant{
		{JID: testLIDOnly, LID: testLIDOnly, PhoneNumber: testLIDPN},
		{JID: testMemberPN, PhoneNumber: testMemberPN},
	}
	got, ok := matchResult(results, testLIDPN)
	if !ok || got.JID != testLIDOnly {
		t.Fatalf("matchResult(PN) = %v, %v; want the LID entry", got.JID, ok)
	}
	if _, ok := matchResult(results, testOutsider); ok {
		t.Fatalf("matchResult(outsider) matched")
	}
}

func TestApplyResult(t *testing.T) {
	exp := time.Unix(1790000000, 0)
	cases := []struct {
		name       string
		action     whatsmeow.ParticipantChange
		res        types.GroupParticipant
		wantStatus string
	}{
		{"added", whatsmeow.ParticipantChangeAdd, types.GroupParticipant{JID: testOutsider}, statusAdded},
		{"removed", whatsmeow.ParticipantChangeRemove, types.GroupParticipant{JID: testOutsider}, statusRemoved},
		{"promoted", whatsmeow.ParticipantChangePromote, types.GroupParticipant{JID: testOutsider}, statusPromoted},
		{"demoted", whatsmeow.ParticipantChangeDemote, types.GroupParticipant{JID: testOutsider}, statusDemoted},
		{"privacy blocked", whatsmeow.ParticipantChangeAdd, types.GroupParticipant{JID: testOutsider, Error: 403, AddRequest: &types.GroupParticipantAddRequest{Code: "abc", Expiration: exp}}, statusInviteRequired},
		{"already member", whatsmeow.ParticipantChangeAdd, types.GroupParticipant{JID: testOutsider, Error: 409}, statusAlreadyMember},
		{"other error", whatsmeow.ParticipantChangeAdd, types.GroupParticipant{JID: testOutsider, Error: 408}, statusFailed},
		{"409 on remove", whatsmeow.ParticipantChangeRemove, types.GroupParticipant{JID: testOutsider, Error: 409}, statusFailed},
	}
	for _, tc := range cases {
		var out ParticipantOutcome
		applyResult(&out, tc.action, tc.res)
		if out.Status != tc.wantStatus {
			t.Errorf("%s: status = %q, want %q", tc.name, out.Status, tc.wantStatus)
		}
		if out.ErrorCode != tc.res.Error {
			t.Errorf("%s: error_code = %d, want %d", tc.name, out.ErrorCode, tc.res.Error)
		}
		if tc.res.AddRequest != nil && !out.InviteExpiresAt.Equal(exp) {
			t.Errorf("%s: invite_expires_at = %v, want %v", tc.name, out.InviteExpiresAt, exp)
		}
	}
}

func TestBuildGroupInviteMessage(t *testing.T) {
	chat := types.NewJID("120363000000000000", types.GroupServer)
	req := &types.GroupParticipantAddRequest{Code: "AbCdEf", Expiration: time.Unix(1790000000, 0)}

	msg := buildGroupInviteMessage(chat, "Test group", "", req)
	inv := msg.GetGroupInviteMessage()
	if inv == nil {
		t.Fatal("GroupInviteMessage not set")
	}
	if inv.GetGroupJID() != chat.String() || inv.GetInviteCode() != "AbCdEf" || inv.GetGroupName() != "Test group" {
		t.Errorf("invite = %v", inv)
	}
	if inv.GetInviteExpiration() != 1790000000 {
		t.Errorf("InviteExpiration = %d, want unix seconds 1790000000", inv.GetInviteExpiration())
	}
	if inv.Caption != nil {
		t.Errorf("Caption = %q, want unset when empty", inv.GetCaption())
	}

	if got := buildGroupInviteMessage(chat, "Test group", "Hai în grup", req).GetGroupInviteMessage().GetCaption(); got != "Hai în grup" {
		t.Errorf("Caption = %q, want %q", got, "Hai în grup")
	}
}

func TestSetGroupName_NotConnected(t *testing.T) {
	c := newDisconnectedClient()
	err := c.SetGroupName(context.Background(), "123@g.us", "hello")
	assertNotConnected(t, err)
}

func TestSetGroupTopic_NotConnected(t *testing.T) {
	c := newDisconnectedClient()
	err := c.SetGroupTopic(context.Background(), "123@g.us", "hi")
	assertNotConnected(t, err)
}

func TestSetGroupAnnounce_NotConnected(t *testing.T) {
	c := newDisconnectedClient()
	err := c.SetGroupAnnounce(context.Background(), "123@g.us", true)
	assertNotConnected(t, err)
}

func TestSetGroupLocked_NotConnected(t *testing.T) {
	c := newDisconnectedClient()
	err := c.SetGroupLocked(context.Background(), "123@g.us", true)
	assertNotConnected(t, err)
}

func TestGetGroupInviteLink_NotConnected(t *testing.T) {
	c := newDisconnectedClient()
	_, err := c.GetGroupInviteLink(context.Background(), "123@g.us", false)
	assertNotConnected(t, err)
}

func TestJoinGroupWithLink_NotConnected(t *testing.T) {
	c := newDisconnectedClient()
	_, err := c.JoinGroupWithLink(context.Background(), "https://chat.whatsapp.com/abc")
	assertNotConnected(t, err)
}

func TestJoinGroupWithLink_RejectsEmpty(t *testing.T) {
	// Guarded by a specific empty-input check that runs after the connection
	// gate, so we can't easily hit it without a live client — but we can at
	// least assert the connection gate fires for a whitespace input.
	c := newDisconnectedClient()
	_, err := c.JoinGroupWithLink(context.Background(), "   ")
	assertNotConnected(t, err)
}

func assertNotConnected(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected not-connected error, got nil")
	}
	if !strings.Contains(err.Error(), "not connected") {
		t.Fatalf("expected not-connected error, got %v", err)
	}
}

func TestPlanParticipantChange_HiddenNumberMember(t *testing.T) {
	// In LID groups a member's number can be hidden (empty PhoneNumber); the
	// stored phone→LID mapping still finds them.
	hiddenLID := types.NewJID("333333333333333", types.HiddenUserServer)
	hiddenPN := types.NewJID("447700000004", types.DefaultUserServer)
	info := testGroupInfo(true)
	info.Participants = append(info.Participants, types.GroupParticipant{JID: hiddenLID, LID: hiddenLID})
	inputs := []string{"447700000004"}
	jids, _ := parseParticipantJIDs(inputs)

	_, pending, err := planParticipantChange(info, &info.Participants[0], inputs, jids, whatsmeow.ParticipantChangeRemove, noLID)
	if err != nil || len(pending) != 0 {
		t.Fatalf("without mapping: pending=%+v err=%v, want not_member and nothing sent", pending, err)
	}
	outcomes, pending, err := planParticipantChange(info, &info.Participants[0], inputs, jids, whatsmeow.ParticipantChangeRemove, lidMap(map[types.JID]types.JID{hiddenPN: hiddenLID}))
	if err != nil {
		t.Fatalf("planParticipantChange error: %v", err)
	}
	if len(pending) != 1 || pending[0].jid != hiddenLID || outcomes[0].JID != hiddenLID.String() {
		t.Fatalf("pending = %+v, outcome = %+v; want the member's LID %s", pending, outcomes[0], hiddenLID)
	}
}

func TestPlanParticipantChange_StrangerAsNumberAndLID(t *testing.T) {
	strangerLID := types.NewJID("444444444444444", types.HiddenUserServer)
	info := testGroupInfo(true)
	inputs := []string{"447700000009", "444444444444444@lid"}
	jids, _ := parseParticipantJIDs(inputs)
	_, _, err := planParticipantChange(info, &info.Participants[0], inputs, jids, whatsmeow.ParticipantChangeAdd, lidMap(map[types.JID]types.JID{testOutsider: strangerLID}))
	if err == nil || !strings.Contains(err.Error(), "same person") {
		t.Fatalf("error = %v, want a same-person rejection", err)
	}
}

func TestParticipantBatches(t *testing.T) {
	pending := []plannedChange{{idx: 0, jid: testOutsider}, {idx: 2, jid: testLIDPN}}
	cases := []struct {
		name   string
		action whatsmeow.ParticipantChange
		mode   types.AddressingMode
		want   int
	}{
		{"add, PN group", whatsmeow.ParticipantChangeAdd, types.AddressingModePN, 1},
		{"add, unknown mode", whatsmeow.ParticipantChangeAdd, "", 1},
		{"add, LID group", whatsmeow.ParticipantChangeAdd, types.AddressingModeLID, 2},
		{"remove, LID group", whatsmeow.ParticipantChangeRemove, types.AddressingModeLID, 1},
	}
	for _, tc := range cases {
		got := participantBatches(pending, tc.action, tc.mode)
		if len(got) != tc.want {
			t.Errorf("%s: %d batches, want %d", tc.name, len(got), tc.want)
		}
		total := 0
		for _, b := range got {
			total += len(b)
		}
		if total != len(pending) {
			t.Errorf("%s: batches hold %d changes, want %d", tc.name, total, len(pending))
		}
	}
}

func TestBuildGroupInviteMessage_NoExpiration(t *testing.T) {
	chat := types.NewJID("120363000000000000", types.GroupServer)
	inv := buildGroupInviteMessage(chat, "Test group", "", &types.GroupParticipantAddRequest{Code: "AbCdEf"}).GetGroupInviteMessage()
	if inv.InviteExpiration != nil {
		t.Errorf("InviteExpiration = %d, want unset when WhatsApp sent none", inv.GetInviteExpiration())
	}
}
