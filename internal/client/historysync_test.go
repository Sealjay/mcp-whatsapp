package client

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.mau.fi/whatsmeow"
	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/proto/waAdv"
	"go.mau.fi/whatsmeow/proto/waHistorySync"
	wmstore "go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"
	"google.golang.org/protobuf/proto"

	"github.com/sealjay/mcp-whatsapp/internal/store"
)

const (
	historyOwnPhone = "447700000009"
	historyOwnLID   = "55443322"
	historyTS       = uint64(1767268800) // 2026-01-01 12:00:00 UTC
)

func TestHistorySyncPollAcrossChunks(t *testing.T) {
	for _, mode := range []string{"cached-key", "key-before-second-handler", "key-in-second-payload", "restart", "startup", "live-poll"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			builder := newPollHistoryClient(t)
			vote := ownPollVoteMsg(t, builder, "Yes")
			sender := types.NewJID("447700000001", types.DefaultUserServer)
			secret, _, err := builder.wa.Store.MsgSecrets.GetMessageSecret(ctx, sender, sender, "POLL-1")
			if err != nil || len(secret) == 0 {
				t.Fatalf("fixture secret: %v", err)
			}
			dir := t.TempDir()
			c, closeReceiver := openPollReceiver(t, dir)
			putSecret := func() {
				t.Helper()
				if err := c.wa.Store.MsgSecrets.PutMessageSecret(ctx, sender, sender, "POLL-1", secret); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "cached-key" {
				putSecret()
			}
			runHistorySync(c, pollChat, vote)
			if _, err := c.GetPollResults(ctx, pollChat, "POLL-1"); !errors.Is(err, sql.ErrNoRows) {
				t.Fatalf("missing poll error: %v", err)
			} else {
				t.Logf("before poll completion: %v", err)
			}
			if mode == "key-before-second-handler" {
				putSecret()
			}
			poll := pollCreationMsg()
			if mode == "key-in-second-payload" || mode == "restart" {
				poll.Message.MessageSecret = secret
			}
			switch mode {
			case "restart":
				closeReceiver()
				c, _ = openPollReceiver(t, dir)
				runHistorySync(c, pollChat, poll)
			case "startup":
				putSecret()
				nm := c.normalizeIncomingMessage(pollChat, pollChat, sender.User, false, false, "", "POLL-1", time.Unix(int64(historyTS), 0), poll.Message.Message)
				if err := c.storeNormalized(ctx, nm, applyPollCreation(&nm, poll.Message.Message)); err != nil {
					t.Fatal(err)
				}
				closeReceiver()
				c, _ = openPollReceiver(t, dir)
				before, err := c.GetPollResults(ctx, pollChat, "POLL-1")
				if err != nil || before.Tally["Yes"] != 0 || pendingPollCount(t, c) != 1 {
					t.Fatalf("getter must not replay pending work: %+v %v", before, err)
				}
				c.StartEventHandler()
			case "live-poll":
				putSecret()
				parsed, err := c.wa.ParseWebMessage(sender, poll.Message)
				if err != nil {
					t.Fatal(err)
				}
				c.handleMessage(parsed)
			default:
				runHistorySync(c, pollChat, poll)
			}
			result, err := c.GetPollResults(ctx, pollChat, "POLL-1")
			if err != nil {
				t.Fatal(err)
			}
			output, err := json.Marshal(result)
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("after recovery: %s", output)
			if string(output) != `{"poll_message_id":"POLL-1","chat_jid":"447700000001@s.whatsapp.net","tally":{"No":0,"Yes":1}}` {
				t.Fatalf("cross-chunk vote lost: %s", output)
			}
			runHistorySync(c, pollChat, vote)
			result, err = c.GetPollResults(ctx, pollChat, "POLL-1")
			if err != nil {
				t.Fatal(err)
			}
			output, err = json.Marshal(result)
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("after duplicate: %s", output)
			if string(output) != `{"poll_message_id":"POLL-1","chat_jid":"447700000001@s.whatsapp.net","tally":{"No":0,"Yes":1}}` {
				t.Fatalf("duplicate changed tally: %s", output)
			}
		})
	}
}

// Both databases share a directory, as in production; nothing connects to WhatsApp.
func openPollReceiver(t *testing.T, dir string) (*Client, func()) {
	t.Helper()
	ctx := context.Background()
	container, err := sqlstore.New(ctx, "sqlite3", "file:"+filepath.Join(dir, "whatsapp.db")+"?_foreign_keys=on", waLog.Noop)
	if err != nil {
		t.Fatal(err)
	}
	device, err := container.GetFirstDevice(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if device.ID == nil {
		device.ID = &types.JID{User: "447700000010", Device: 3, Server: types.DefaultUserServer}
		device.Account = &waAdv.ADVSignedDeviceIdentity{Details: []byte{}, AccountSignature: make([]byte, 64), AccountSignatureKey: make([]byte, 32), DeviceSignature: make([]byte, 64)}
		if err := device.Save(ctx); err != nil {
			t.Fatal(err)
		}
	}
	s, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	c := newClientWithStore(t, s)
	c.wa = whatsmeow.NewClient(device, NewStderrLogger("test", "ERROR", false))
	if err := s.StoreChat(pollChat, "Alex", time.Now()); err != nil {
		t.Fatal(err)
	}
	closed := false
	closeReceiver := func() {
		if closed {
			return
		}
		closed = true
		if err := s.Close(); err != nil {
			t.Error(err)
		}
		if err := container.Close(); err != nil {
			t.Error(err)
		}
	}
	t.Cleanup(closeReceiver)
	return c, closeReceiver
}

func pendingPollCount(t *testing.T, c *Client) int {
	t.Helper()
	rows, err := c.store.PendingPollVotes(context.Background(), c.wa.Store.ID.ToNonAD().String(), nil)
	if err != nil {
		t.Fatal(err)
	}
	return len(rows)
}

func TestHistorySyncPollReplayOrdering(t *testing.T) {
	ctx := context.Background()
	builder := newPollHistoryClient(t)
	for _, mode := range []string{"changed-then-withdrawn", "older-before-aggregated-and-revoke", "future-clamp"} {
		t.Run(mode, func(t *testing.T) {
			c, _ := openPollReceiver(t, t.TempDir())
			yes := ownPollVoteMsg(t, builder, "Yes")
			if mode == "future-clamp" {
				yes.Message.MessageTimestamp = proto.Uint64(uint64(time.Now().Add(24 * time.Hour).Unix()))
			}
			runHistorySync(c, pollChat, yes)
			rows, err := c.store.PendingPollVotes(ctx, c.wa.Store.ID.ToNonAD().String(), nil)
			if err != nil || len(rows) != 1 {
				t.Fatalf("pending: %v %v", rows, err)
			}
			first := rows[0]
			runHistorySync(c, pollChat, yes)
			rows, err = c.store.PendingPollVotes(ctx, first.OwnerJID, nil)
			if err != nil || len(rows) != 1 || !rows[0].Timestamp.Equal(first.Timestamp) || !rows[0].ReceivedAt.Equal(first.ReceivedAt) {
				t.Fatalf("duplicate advanced receipt/clamp: %v %v", rows, err)
			}
			poll := pollCreationMsg()
			poll.Message.MessageSecret = append([]byte{1}, make([]byte, 31)...)
			switch mode {
			case "changed-then-withdrawn":
				runHistorySync(c, pollChat, poll)
				no := ownPollVoteMsg(t, builder, "No")
				no.Message.Key.ID = proto.String("VOTE-2")
				no.Message.MessageTimestamp = proto.Uint64(historyTS + 1)
				runHistorySync(c, pollChat, no, yes)
				if got := pollTally(t, c.store); got["No"] != 1 || got["Yes"] != 0 {
					t.Fatalf("changed: %v", got)
				}
				withdraw := ownPollVoteMsg(t, builder)
				withdraw.Message.Key.ID = proto.String("VOTE-3")
				withdraw.Message.MessageTimestamp = proto.Uint64(historyTS + 2)
				runHistorySync(c, pollChat, withdraw, no)
				if got := pollTally(t, c.store); got["No"] != 0 || got["Yes"] != 0 {
					t.Fatalf("withdrawn: %v", got)
				}
			case "older-before-aggregated-and-revoke":
				poll.Message.PollUpdates = []*waProto.PollUpdate{{
					PollUpdateMessageKey: historyKey(pollChat, "attached", true, ""),
					Vote:                 &waProto.PollVoteMessage{SelectedOptions: whatsmeow.HashPollOptions([]string{"No"})},
					SenderTimestampMS:    proto.Int64(int64(historyTS) * 1000),
				}}
				revoke := historyMsg(historyKey(pollChat, "revoke", false, ""), &waProto.Message{
					ProtocolMessage: &waProto.ProtocolMessage{Type: waProto.ProtocolMessage_REVOKE.Enum(), Key: historyKey(pollChat, "POLL-1", false, "")},
				})
				revoke.Message.MessageTimestamp = proto.Uint64(historyTS + 1)
				runHistorySync(c, pollChat, revoke, poll)
				if got := pollTally(t, c.store); got["No"] != 1 || got["Yes"] != 0 {
					t.Fatalf("vote/revoke order: %v", got)
				}
				if !storedRevoked(t, c.store, "POLL-1") {
					t.Fatal("poll not revoked")
				}
			case "future-clamp":
				// A later local vote must survive replay of the old future-dated
				// update: its ordering time is fixed at the first receipt.
				nm := c.normalizeIncomingMessage(pollChat, pollChat, "447700000001", false, false, "", "POLL-1", time.Unix(int64(historyTS), 0), poll.Message.Message)
				if err := c.storeNormalized(ctx, nm, applyPollCreation(&nm, poll.Message.Message)); err != nil {
					t.Fatal(err)
				}
				if err := c.store.StorePollVote(ctx, "POLL-1", pollChat, first.OwnerJID, []string{"No"}, first.Timestamp.Add(time.Nanosecond)); err != nil {
					t.Fatal(err)
				}
				runHistorySync(c, pollChat, poll)
				if got := pollTally(t, c.store); got["No"] != 1 || got["Yes"] != 0 {
					t.Fatalf("replay advanced clamp: %v", got)
				}
			}
			if n := pendingPollCount(t, c); n != 0 {
				t.Fatalf("unacknowledged votes: %d", n)
			}
		})
	}
}

func TestHistorySyncPollMappingOnlyRecovery(t *testing.T) {
	ctx := context.Background()
	builder := newPollHistoryClient(t)
	for _, malformedPosition := range []string{"before", "after"} {
		t.Run(malformedPosition, func(t *testing.T) {
			c, closeReceiver := openPollReceiver(t, t.TempDir())
			const lidChat = "9988776655@lid"
			if err := c.store.StoreChat(lidChat, "Alex", time.Now()); err != nil {
				t.Fatal(err)
			}
			poll := pollCreationMsg()
			poll.Message.Key.RemoteJID = proto.String(lidChat)
			poll.Message.MessageSecret = append([]byte{1}, make([]byte, 31)...)
			runHistorySync(c, lidChat, poll)
			vote := ownPollVoteMsg(t, builder, "Yes")
			runHistorySync(c, pollChat, vote)
			if pendingPollCount(t, c) != 1 {
				t.Fatal("vote should wait for verified alias")
			}
			rows, err := c.store.PendingPollVotes(ctx, c.wa.Store.ID.ToNonAD().String(), nil)
			if err != nil {
				t.Fatal(err)
			}
			other := rows[0]
			other.OwnerJID = "447700000011@s.whatsapp.net"
			if _, _, err := c.store.QueuePendingPollVote(ctx, other); err != nil {
				t.Fatal(err)
			}
			dir := c.store.Dir()
			closeReceiver()
			c, _ = openPollReceiver(t, dir)
			var logs bytes.Buffer
			c.log = &stderrLogger{out: &logs, mod: "test", min: logLevelToInt["DEBUG"]}
			mapping := &waHistorySync.PhoneNumberToLIDMapping{PnJID: proto.String("447700000001@c.us"), LidJID: proto.String(lidChat)}
			data := &waProto.HistorySync{PhoneNumberToLidMappings: []*waHistorySync.PhoneNumberToLIDMapping{mapping}}
			malformed := &waHistorySync.PhoneNumberToLIDMapping{
				PnJID: proto.String("447700009999@g.us"), LidJID: proto.String("9988776699@lid"),
			}
			if malformedPosition == "before" {
				data.PhoneNumberToLidMappings = append([]*waHistorySync.PhoneNumberToLIDMapping{malformed}, data.PhoneNumberToLidMappings...)
			} else {
				data.PhoneNumberToLidMappings = append(data.PhoneNumberToLidMappings, malformed)
			}
			before := proto.Clone(data)
			c.handleHistorySync(&events.HistorySync{Data: data})
			if !proto.Equal(before, data) {
				t.Fatal("history mapping input mutated")
			}
			result, err := c.GetPollResults(ctx, pollChat, "POLL-1")
			if err != nil || result.Tally["Yes"] != 1 {
				t.Fatalf("mapping-only recovery: %+v %v", result, err)
			}
			if !strings.Contains(logs.String(), "Skipping invalid history PN/LID mapping") {
				t.Fatalf("missing malformed-pair warning: %s", logs.String())
			}
			for _, private := range []string{"447700009999", "9988776699"} {
				if strings.Contains(logs.String(), private) {
					t.Fatalf("unredacted malformed mapping: %s", logs.String())
				}
			}
			var actual string
			if err := c.store.DB().QueryRow("SELECT poll_chat_jid FROM poll_votes").Scan(&actual); err != nil || actual != lidChat {
				t.Fatalf("actual cached key: %s %v", actual, err)
			}
			if pendingPollCount(t, c) != 0 {
				t.Fatal("owner's vote not acknowledged")
			}
			otherRows, err := c.store.PendingPollVotes(ctx, other.OwnerJID, nil)
			if err != nil || len(otherRows) != 1 {
				t.Fatalf("wrong owner's row consumed: %v %v", otherRows, err)
			}
			if _, err := c.GetPollResults(ctx, "447700000099@s.whatsapp.net", "POLL-1"); !errors.Is(err, sql.ErrNoRows) {
				t.Fatalf("poll ID escaped chat: %v", err)
			}
		})
	}
}

func TestHistorySyncPollDependencyFailureDoesNotSkipReadyVoteBeforeRevoke(t *testing.T) {
	ctx := context.Background()
	for _, failure := range []string{"other-poll-secret", "unrelated-mapping"} {
		t.Run(failure, func(t *testing.T) {
			builder := newPollHistoryClient(t)
			c, _ := openPollReceiver(t, t.TempDir())
			runHistorySync(c, pollChat, ownPollVoteMsg(t, builder, "Yes"))
			sender := types.NewJID("447700000001", types.DefaultUserServer)
			secret := append([]byte{1}, make([]byte, 31)...)
			if err := builder.wa.Store.MsgSecrets.PutMessageSecret(ctx, sender, sender, "POLL-2", secret); err != nil {
				t.Fatal(err)
			}
			secondVote, err := builder.wa.BuildPollVote(ctx, &types.MessageInfo{
				ID: "POLL-2", MessageSource: types.MessageSource{Chat: sender, Sender: sender},
			}, []string{"Yes"})
			if err != nil {
				t.Fatal(err)
			}
			if failure == "other-poll-secret" {
				runHistorySync(c, pollChat, historyMsg(historyKey(pollChat, "VOTE-2", true, ""), secondVote))
			}
			waDB, err := sql.Open("sqlite3", filepath.Join(c.store.Dir(), "whatsapp.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer waDB.Close()
			trigger := `CREATE TRIGGER fail_dependency BEFORE INSERT ON whatsmeow_message_secrets
				WHEN NEW.message_id = 'POLL-2' BEGIN SELECT RAISE(FAIL, 'second poll secret blocked'); END`
			if failure == "unrelated-mapping" {
				trigger = `CREATE TRIGGER fail_dependency BEFORE INSERT ON whatsmeow_lid_map
					WHEN NEW.lid = '9988776655' BEGIN SELECT RAISE(FAIL, 'unrelated mapping blocked'); END`
			}
			if _, err := waDB.Exec(trigger); err != nil {
				t.Fatal(err)
			}
			first, second := pollCreationMsg(), pollCreationMsg()
			first.Message.MessageSecret = secret
			second.Message.Key.ID = proto.String("POLL-2")
			second.Message.MessageSecret = secret
			revoke := historyMsg(historyKey(pollChat, "REVOKE-1", false, ""), &waProto.Message{
				ProtocolMessage: &waProto.ProtocolMessage{
					Type: waProto.ProtocolMessage_REVOKE.Enum(), Key: historyKey(pollChat, "POLL-1", false, ""),
				},
			})
			revoke.Message.MessageTimestamp = proto.Uint64(historyTS + 1)
			data := &waProto.HistorySync{Conversations: []*waProto.Conversation{{
				ID: proto.String(pollChat), Messages: []*waProto.HistorySyncMsg{revoke, second, first},
			}}}
			if failure == "unrelated-mapping" {
				data.PhoneNumberToLidMappings = []*waHistorySync.PhoneNumberToLIDMapping{{
					PnJID: proto.String("447700000099@s.whatsapp.net"), LidJID: proto.String("9988776655@lid"),
				}}
			}
			c.handleHistorySync(&events.HistorySync{Data: data})
			if !storedRevoked(t, c.store, "POLL-1") {
				t.Fatal("poll not revoked")
			}
			got := pollTally(t, c.store)
			if got["Yes"] != 1 || got["No"] != 0 {
				t.Fatalf("ready vote lost before revoke after %s failure: %v", failure, got)
			}
			wantPending := 0
			if failure == "other-poll-secret" {
				wantPending = 1
			}
			if n := pendingPollCount(t, c); n != wantPending {
				t.Fatalf("affected pending votes: got %d, want %d", n, wantPending)
			}
			if _, err := waDB.Exec("DROP TRIGGER fail_dependency"); err != nil {
				t.Fatal(err)
			}
			runHistorySync(c, pollChat, second)
			if pendingPollCount(t, c) != 0 {
				t.Fatal("affected vote not recovered")
			}
			if got := pollTally(t, c.store); got["Yes"] != 1 {
				t.Fatalf("later retry changed revoked poll tally: %v", got)
			}
		})
	}
}

func TestHistorySyncPollStorageFailuresRetainPending(t *testing.T) {
	builder := newPollHistoryClient(t)
	for _, mode := range []string{"metadata", "secret", "tally", "mapping", "decrypt-db", "decrypt-auth"} {
		t.Run(mode, func(t *testing.T) {
			c, _ := openPollReceiver(t, t.TempDir())
			var logs bytes.Buffer
			c.log = &stderrLogger{out: &logs, mod: "test", min: logLevelToInt["DEBUG"]}
			runHistorySync(c, pollChat, ownPollVoteMsg(t, builder, "Yes"))
			waDB, err := sql.Open("sqlite3", filepath.Join(c.store.Dir(), "whatsapp.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer waDB.Close()
			poll := pollCreationMsg()
			poll.Message.MessageSecret = append([]byte{1}, make([]byte, 31)...)
			var undo func()
			switch mode {
			case "metadata":
				_, err = c.store.DB().Exec(`CREATE TRIGGER fail_metadata BEFORE UPDATE OF poll_options_json ON messages BEGIN SELECT RAISE(FAIL,'blocked 447700000001'); END`)
				undo = func() { _, err = c.store.DB().Exec("DROP TRIGGER fail_metadata") }
			case "secret":
				_, err = waDB.Exec(`CREATE TRIGGER fail_secret BEFORE INSERT ON whatsmeow_message_secrets BEGIN SELECT RAISE(FAIL,'blocked 447700000001'); END`)
				undo = func() { _, err = waDB.Exec("DROP TRIGGER fail_secret") }
			case "tally":
				_, err = c.store.DB().Exec(`CREATE TRIGGER fail_tally BEFORE INSERT ON poll_votes BEGIN SELECT RAISE(FAIL,'blocked 447700000001'); END`)
				undo = func() { _, err = c.store.DB().Exec("DROP TRIGGER fail_tally") }
			case "mapping":
				// This vote genuinely needs the mapping: its poll and secret
				// are cached under a LID, while the update arrived in the PN chat.
				const lidChat = "9988776655@lid"
				poll.Message.Key.RemoteJID = proto.String(lidChat)
				if err := c.store.StoreChat(lidChat, "Alex", time.Now()); err != nil {
					t.Fatal(err)
				}
				runHistorySync(c, lidChat, poll)
				_, err = waDB.Exec(`CREATE TRIGGER fail_mapping BEFORE INSERT ON whatsmeow_lid_map BEGIN SELECT RAISE(FAIL,'blocked 447700000001'); END`)
				undo = func() { _, err = waDB.Exec("DROP TRIGGER fail_mapping") }
			case "decrypt-db":
				_, err = waDB.Exec("ALTER TABLE whatsmeow_message_secrets RENAME TO unavailable_secrets")
				poll.Message.MessageSecret = nil
				undo = func() {
					_, err = waDB.Exec("ALTER TABLE unavailable_secrets RENAME TO whatsmeow_message_secrets")
					poll.Message.MessageSecret = append([]byte{1}, make([]byte, 31)...)
				}
			case "decrypt-auth":
				poll.Message.MessageSecret = append([]byte{99}, make([]byte, 31)...)
				undo = func() {
					_, err = waDB.Exec("DELETE FROM whatsmeow_message_secrets")
					poll.Message.MessageSecret = append([]byte{1}, make([]byte, 31)...)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			run := func() {
				if mode == "mapping" {
					c.handleHistorySync(&events.HistorySync{Data: &waProto.HistorySync{
						PhoneNumberToLidMappings: []*waHistorySync.PhoneNumberToLIDMapping{{PnJID: proto.String(pollChat), LidJID: proto.String("9988776655@lid")}},
					}})
				} else {
					runHistorySync(c, pollChat, poll)
				}
			}
			run()
			if pendingPollCount(t, c) != 1 {
				t.Fatal("storage/decrypt failure acknowledged vote")
			}
			var count int
			if err := c.store.DB().QueryRow("SELECT count(*) FROM poll_votes").Scan(&count); err != nil || count != 0 {
				t.Fatalf("partial tally: %d %v", count, err)
			}
			if !strings.Contains(logs.String(), "WARN") {
				t.Fatalf("failure not warned: %s", logs.String())
			}
			if strings.Contains(logs.String(), "447700000001") {
				t.Fatalf("unredacted error: %s", logs.String())
			}
			undo()
			if err != nil {
				t.Fatal(err)
			}
			run()
			if got := pollTally(t, c.store); got["Yes"] != 1 || got["No"] != 0 {
				t.Fatalf("retry failed: %v", got)
			}
			if pendingPollCount(t, c) != 0 {
				t.Fatal("successful retry not acknowledged")
			}
		})
	}
}

func TestHistorySyncPollMalformedAndCapacityWarnings(t *testing.T) {
	ctx := context.Background()
	builder := newPollHistoryClient(t)
	c, _ := openPollReceiver(t, t.TempDir())
	var logs bytes.Buffer
	c.log = &stderrLogger{out: &logs, mod: "test", min: logLevelToInt["DEBUG"]}
	vote := ownPollVoteMsg(t, builder, "Yes")
	runHistorySync(c, pollChat, vote)
	if strings.Contains(logs.String(), "WARN") || !strings.Contains(logs.String(), "deferred") {
		t.Fatalf("missing dependency should defer at debug: %s", logs.String())
	}
	for i, payload := range [][]byte{{0xff}, {0x08, 0x01}} {
		_, _, err := c.store.QueuePendingPollVote(ctx, store.PendingPollVote{
			OwnerJID: c.wa.Store.ID.ToNonAD().String(), ChatJID: pollChat, SenderJID: pollChat,
			MessageID: fmt.Sprint("malformed-", i), Timestamp: time.Now(), Payload: payload,
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	c.StartEventHandler()
	if pendingPollCount(t, c) != 1 || !strings.Contains(logs.String(), "Dropping malformed") {
		t.Fatalf("malformed snapshots not dropped: %s", logs.String())
	}
	// Pressure is global, including rows from a different owner; those rows
	// must never be decrypted by this receiver.
	_, err := c.store.DB().Exec(`WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i < 999)
					INSERT INTO pending_poll_votes(owner_jid,chat_jid,sender_jid,message_id,timestamp,received_at,payload)
					SELECT ?, ?, ?, 'other-' || i, ?, ?, x'01' FROM n`,
		"447700000011@s.whatsapp.net", pollChat, pollChat, time.Now().Add(-time.Hour), time.Now().UTC().Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	vote.Message.Key.ID = proto.String("capacity-vote")
	runHistorySync(c, pollChat, vote)
	var count int
	if err := c.store.DB().QueryRow("SELECT count(*) FROM pending_poll_votes").Scan(&count); err != nil || count != 1000 {
		t.Fatalf("capacity: %d %v", count, err)
	}
	if !strings.Contains(logs.String(), "Evicted 1 oldest pending poll votes at capacity; cached totals may be incomplete") {
		t.Fatalf("missing eviction warning: %s", logs.String())
	}
	vote.Message.Key.ID = proto.String("oversized-vote")
	vote.Message.Message.PollUpdateMessage.Vote.EncPayload = make([]byte, 16384)
	runHistorySync(c, pollChat, vote)
	if !strings.Contains(logs.String(), "exceeds 16 KiB; cached totals may be incomplete") {
		t.Fatalf("missing size warning: %s", logs.String())
	}
	for _, sensitive := range []string{"447700000001", "447700000010", "447700000011", "capacity-vote", "oversized-vote", "Lunch?", "Yes"} {
		if strings.Contains(logs.String(), sensitive) {
			t.Fatalf("private content in logs: %s", logs.String())
		}
	}
}
func TestHistorySyncPollGroupRawSender(t *testing.T) {
	ctx := context.Background()
	for _, fromMe := range []bool{false, true} {
		t.Run(fmt.Sprint("from-me-", fromMe), func(t *testing.T) {
			builder := newPollHistoryClient(t)
			builder.wa.Store.LID = types.NewJID("5544332211", types.HiddenUserServer)
			chat := types.NewJID("120363123456789", types.GroupServer)
			creator := types.NewJID("9988776655", types.HiddenUserServer)
			secret := append([]byte{2}, make([]byte, 31)...)
			if err := builder.wa.Store.MsgSecrets.PutMessageSecret(ctx, chat, creator, "POLL-1", secret); err != nil {
				t.Fatal(err)
			}
			encrypted, err := builder.wa.BuildPollVote(ctx, &types.MessageInfo{ID: "POLL-1", MessageSource: types.MessageSource{Chat: chat, Sender: creator, IsGroup: true}}, []string{"Yes"})
			if err != nil {
				t.Fatal(err)
			}
			c, _ := openPollReceiver(t, t.TempDir())
			if err := c.store.StoreChat(chat.String(), "Team", time.Now()); err != nil {
				t.Fatal(err)
			}
			vote := historyMsg(historyKey(chat.String(), "group-vote", fromMe, "447700000099@s.whatsapp.net"), encrypted)
			rawSender := "5544332211:7@lid"
			if fromMe {
				vote.Message.OriginalSelfAuthorUserJIDString = proto.String(rawSender)
			} else {
				vote.Message.Participant = proto.String(rawSender)
			}
			runHistorySync(c, chat.String(), vote)
			rows, err := c.store.PendingPollVotes(ctx, c.wa.Store.ID.ToNonAD().String(), nil)
			if err != nil || len(rows) != 1 || rows[0].SenderJID != rawSender {
				t.Fatalf("raw parsed crypto sender lost: %v %v", rows, err)
			}
			poll := pollCreationMsg()
			poll.Message.Key = historyKey(chat.String(), "POLL-1", false, creator.String())
			poll.Message.Participant = proto.String("447700000099@s.whatsapp.net")
			poll.Message.MessageSecret = secret
			c.handleHistorySync(&events.HistorySync{Data: &waProto.HistorySync{
				PhoneNumberToLidMappings: []*waHistorySync.PhoneNumberToLIDMapping{{
					PnJID: proto.String("447700000010@s.whatsapp.net"), LidJID: proto.String("5544332211@lid"),
				}},
				Conversations: []*waProto.Conversation{{ID: proto.String(chat.String()), Messages: []*waProto.HistorySyncMsg{poll}}},
			}})
			result, err := c.GetPollResults(ctx, chat.String(), "POLL-1")
			if err != nil || result.Tally["Yes"] != 1 {
				t.Fatalf("raw group replay: %+v %v", result, err)
			}
			var voter string
			if err := c.store.DB().QueryRow("SELECT voter_jid FROM poll_votes").Scan(&voter); err != nil || voter != "447700000010@s.whatsapp.net" {
				t.Fatalf("normalised tally voter: %s %v", voter, err)
			}
		})
	}
}

// newHistoryTestClient returns a Client whose whatsmeow client is real but
// never connected: its device carries only our own phone JID and LID, which
// is all handleHistorySync and ParseWebMessage read from it. Chats must be
// pre-seeded with names so GetChatName never reaches the (absent) contact
// store or the network.
func newHistoryTestClient(t *testing.T, s *store.Store) *Client {
	t.Helper()
	own := types.NewADJID(historyOwnPhone, 0, 12)
	device := &wmstore.Device{ID: &own, LID: types.NewJID(historyOwnLID, types.HiddenUserServer)}
	c := newClientWithStore(t, s)
	c.wa = whatsmeow.NewClient(device, NewStderrLogger("test", "ERROR", false))
	return c
}

// historyMsg builds a history-sync entry as WhatsApp sends it.
func historyMsg(key *waProto.MessageKey, m *waProto.Message) *waProto.HistorySyncMsg {
	return &waProto.HistorySyncMsg{Message: &waProto.WebMessageInfo{
		Key:              key,
		Message:          m,
		MessageTimestamp: proto.Uint64(historyTS),
	}}
}

func historyKey(chat, id string, fromMe bool, participant string) *waProto.MessageKey {
	k := &waProto.MessageKey{RemoteJID: proto.String(chat), ID: proto.String(id), FromMe: proto.Bool(fromMe)}
	if participant != "" {
		k.Participant = proto.String(participant)
	}
	return k
}

func runHistorySync(c *Client, chat string, msgs ...*waProto.HistorySyncMsg) {
	c.handleHistorySync(&events.HistorySync{Data: &waProto.HistorySync{
		SyncType:      waProto.HistorySync_RECENT.Enum(),
		Conversations: []*waProto.Conversation{{ID: proto.String(chat), Messages: msgs}},
	}})
}

type storedRow struct {
	chatJID, sender, content, mediaType, filename string
	isFromMe                                      bool
}

func queryStored(t *testing.T, s *store.Store, id string) (storedRow, bool) {
	t.Helper()
	var r storedRow
	err := s.DB().QueryRow(
		"SELECT chat_jid, sender, content, media_type, filename, is_from_me FROM messages WHERE id = ?", id,
	).Scan(&r.chatJID, &r.sender, &r.content, &r.mediaType, &r.filename, &r.isFromMe)
	if err != nil {
		return storedRow{}, false
	}
	return r, true
}

func mustStored(t *testing.T, s *store.Store, id string) storedRow {
	t.Helper()
	r, ok := queryStored(t, s, id)
	if !ok {
		t.Fatalf("expected a stored row for %s", id)
	}
	return r
}

func TestHandleHistorySync_UnwrapsWrappedMessages(t *testing.T) {
	s := newTestStoreWithLIDMap(t, nil)
	c := newHistoryTestClient(t, s)

	const chat = "447700000001@s.whatsapp.net"
	if err := s.StoreChat(chat, "Alice", time.Now()); err != nil {
		t.Fatalf("seed chat: %v", err)
	}

	runHistorySync(c, chat,
		historyMsg(historyKey(chat, "eph-1", false, ""), &waProto.Message{
			EphemeralMessage: &waProto.FutureProofMessage{Message: &waProto.Message{
				ExtendedTextMessage: &waProto.ExtendedTextMessage{Text: proto.String("disappearing hello")},
			}},
		}),
		historyMsg(historyKey(chat, "vo-1", false, ""), &waProto.Message{
			ViewOnceMessageV2: &waProto.FutureProofMessage{Message: &waProto.Message{
				ImageMessage: &waProto.ImageMessage{
					URL:        proto.String("https://mmg.whatsapp.net/v/t62/img.enc"),
					DirectPath: proto.String("/v/t62/img.enc?ccb=1"),
					MediaKey:   []byte{0x01},
					FileSHA256: []byte{0xab, 0xcd},
				},
			}},
		}),
		historyMsg(historyKey(chat, "doc-1", false, ""), &waProto.Message{
			DocumentWithCaptionMessage: &waProto.FutureProofMessage{Message: &waProto.Message{
				DocumentMessage: &waProto.DocumentMessage{
					FileName:   proto.String("report.pdf"),
					Caption:    proto.String("see attached"),
					DirectPath: proto.String("/v/t62/doc.enc?ccb=1"),
					MediaKey:   []byte{0x02},
				},
			}},
		}),
	)

	if r := mustStored(t, s, "eph-1"); r.content != "disappearing hello" || r.sender != "447700000001" || r.isFromMe {
		t.Errorf("ephemeral text stored as %+v", r)
	}
	if r := mustStored(t, s, "vo-1"); r.mediaType != "image" || r.filename != "image_abcd.jpg" || r.chatJID != chat {
		t.Errorf("view-once image stored as %+v", r)
	}
	if r := mustStored(t, s, "doc-1"); r.mediaType != "document" || r.filename != "report.pdf" {
		t.Errorf("document with caption stored as %+v", r)
	}
}

func TestHandleHistorySync_FromMe(t *testing.T) {
	// A LID-addressed DM: from-me rows keep the phone-number sender that
	// sent messages use, wrapped or not.
	s := newTestStoreWithLIDMap(t, map[string]string{"99887766": "447700000002"})
	c := newHistoryTestClient(t, s)

	const resolved = "447700000002@s.whatsapp.net"
	if err := s.StoreChat(resolved, "Bob", time.Now()); err != nil {
		t.Fatalf("seed chat: %v", err)
	}

	runHistorySync(c, "99887766@lid",
		historyMsg(historyKey("99887766@lid", "me-1", true, ""), &waProto.Message{
			Conversation: proto.String("sent from my phone"),
		}),
		historyMsg(historyKey("99887766@lid", "me-2", true, ""), &waProto.Message{
			DeviceSentMessage: &waProto.DeviceSentMessage{
				DestinationJID: proto.String("99887766@lid"),
				Message:        &waProto.Message{Conversation: proto.String("sent from another device")},
			},
		}),
	)

	for id, want := range map[string]string{"me-1": "sent from my phone", "me-2": "sent from another device"} {
		r := mustStored(t, s, id)
		if !r.isFromMe || r.sender != historyOwnPhone || r.chatJID != resolved || r.content != want {
			t.Errorf("%s stored as %+v, want from-me row by %s in %s with %q", id, r, historyOwnPhone, resolved, want)
		}
	}
}

func TestHandleHistorySync_GroupParticipant(t *testing.T) {
	s := newTestStoreWithLIDMap(t, map[string]string{"99887766": "447700000002"})
	c := newHistoryTestClient(t, s)

	const group = "123456789@g.us"
	if err := s.StoreChat(group, "Project Team", time.Now()); err != nil {
		t.Fatalf("seed chat: %v", err)
	}

	// Participant in the key (LID, resolved to a phone) and, as some
	// history payloads carry it, only in WebMessageInfo.participant.
	topLevel := historyMsg(historyKey(group, "grp-2", false, ""), &waProto.Message{Conversation: proto.String("second")})
	topLevel.Message.Participant = proto.String("447700000003@s.whatsapp.net")

	runHistorySync(c, group,
		historyMsg(historyKey(group, "grp-1", false, "99887766@lid"), &waProto.Message{
			EphemeralMessage: &waProto.FutureProofMessage{Message: &waProto.Message{Conversation: proto.String("first")}},
		}),
		topLevel,
	)

	if r := mustStored(t, s, "grp-1"); r.chatJID != group || r.sender != "447700000002" || r.content != "first" || r.isFromMe {
		t.Errorf("grp-1 stored as %+v", r)
	}
	if r := mustStored(t, s, "grp-2"); r.sender != "447700000003" || r.content != "second" {
		t.Errorf("grp-2 stored as %+v", r)
	}
}

func TestHandleHistorySync_GroupParticipantResync(t *testing.T) {
	s := newTestStoreWithLIDMap(t, map[string]string{"99887766": "447700000002"})
	c := newHistoryTestClient(t, s)

	const group = "123456789-1234567890@g.us"
	if err := s.StoreChat(group, "Project Team", time.Now()); err != nil {
		t.Fatalf("seed chat: %v", err)
	}

	msg := historyMsg(historyKey(group, "resync-1", false, ""), &waProto.Message{Conversation: proto.String("hello")})
	runHistorySync(c, group, msg)
	if r := mustStored(t, s, "resync-1"); r.sender != "123456789-1234567890" {
		t.Fatalf("initial sender = %q, want group fallback", r.sender)
	}

	msg.Message.Participant = proto.String("99887766@lid")
	keyFirst := historyMsg(historyKey(group, "key-first", false, "447700000003@s.whatsapp.net"), &waProto.Message{Conversation: proto.String("key sender")})
	keyFirst.Message.Participant = proto.String("99887766@lid")
	own := historyMsg(historyKey(group, "own", true, ""), &waProto.Message{Conversation: proto.String("my message")})
	own.Message.Participant = proto.String("99887766@lid")
	runHistorySync(c, group, msg, keyFirst, own)

	if r := mustStored(t, s, "resync-1"); r.chatJID != group || r.sender != "447700000002" || r.content != "hello" || r.isFromMe {
		t.Errorf("resynced message stored as %+v", r)
	}
	if r := mustStored(t, s, "key-first"); r.sender != "447700000003" || r.isFromMe {
		t.Errorf("key participant message stored as %+v", r)
	}
	if r := mustStored(t, s, "own"); r.sender != historyOwnPhone || !r.isFromMe {
		t.Errorf("own message stored as %+v", r)
	}

	messages, err := s.ListMessages(context.Background(), store.ListMessagesParams{ChatJID: group, SenderPhone: "447700000002"})
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	if len(messages) != 1 || messages[0].ID != "resync-1" {
		t.Errorf("filtered messages = %+v, want only resync-1", messages)
	}
}

func TestHandleHistorySync_AppliesEditsAndRevokes(t *testing.T) {
	s := newTestStoreWithLIDMap(t, nil)
	c := newHistoryTestClient(t, s)

	const (
		group = "123456789@g.us"
		bob   = "447700000002@s.whatsapp.net"
		carol = "447700000003@s.whatsapp.net"
	)
	if err := s.StoreChat(group, "Project Team", time.Now()); err != nil {
		t.Fatalf("seed chat: %v", err)
	}

	edit := func(key *waProto.MessageKey, body string) *waProto.Message {
		return &waProto.Message{EditedMessage: &waProto.FutureProofMessage{Message: &waProto.Message{
			ProtocolMessage: &waProto.ProtocolMessage{
				Type:          waProto.ProtocolMessage_MESSAGE_EDIT.Enum(),
				Key:           key,
				EditedMessage: &waProto.Message{Conversation: proto.String(body)},
			},
		}}}
	}
	revoke := func(key *waProto.MessageKey) *waProto.Message {
		return &waProto.Message{ProtocolMessage: &waProto.ProtocolMessage{Type: waProto.ProtocolMessage_REVOKE.Enum(), Key: key}}
	}
	at := func(m *waProto.HistorySyncMsg, offset uint64) *waProto.HistorySyncMsg {
		m.Message.MessageTimestamp = proto.Uint64(historyTS + offset)
		return m
	}

	// History arrives newest first, so the edits and the revoke precede the
	// messages they change. Bob's own edit names his message as from-me (as
	// sent) or by participant; both apply. Carol's forgeries do not.
	runHistorySync(c, group,
		at(historyMsg(historyKey(group, "setting-1", false, bob), &waProto.Message{ProtocolMessage: &waProto.ProtocolMessage{
			Type: waProto.ProtocolMessage_EPHEMERAL_SETTING.Enum(), EphemeralExpiration: proto.Uint32(86400),
		}}), 50),
		at(historyMsg(historyKey(group, "forge-1", false, carol), edit(historyKey(group, "orig-1", true, ""), "carol was here")), 40),
		at(historyMsg(historyKey(group, "forge-2", false, carol), revoke(historyKey(group, "orig-3", true, ""))), 40),
		at(historyMsg(historyKey(group, "revoke-1", false, bob), revoke(historyKey(group, "orig-2", false, bob))), 30),
		at(historyMsg(historyKey(group, "edit-2", false, bob), edit(historyKey(group, "orig-1", false, bob), "edited twice")), 20),
		at(historyMsg(historyKey(group, "edit-1", false, bob), edit(historyKey(group, "orig-1", true, ""), "edited once")), 10),
		historyMsg(historyKey(group, "orig-3", false, bob), &waProto.Message{Conversation: proto.String("third")}),
		historyMsg(historyKey(group, "orig-2", false, bob), &waProto.Message{Conversation: proto.String("to be deleted")}),
		historyMsg(historyKey(group, "orig-1", false, bob), &waProto.Message{Conversation: proto.String("original text")}),
	)

	if r := mustStored(t, s, "orig-1"); r.content != "edited twice" {
		t.Errorf("orig-1 content = %q, want the latest edit", r.content)
	}
	if r := mustStored(t, s, "orig-2"); r.content != "to be deleted" || !storedRevoked(t, s, "orig-2") {
		t.Errorf("orig-2 = %+v, want it marked deleted with its text kept", r)
	}
	if r := mustStored(t, s, "orig-3"); r.content != "third" || storedRevoked(t, s, "orig-3") {
		t.Errorf("orig-3 = %+v, Carol must not revoke Bob's message", r)
	}
	for _, id := range []string{"edit-1", "edit-2", "revoke-1", "forge-1", "forge-2", "setting-1"} {
		if r, ok := queryStored(t, s, id); ok {
			t.Errorf("%s should not be stored as a message, stored as %+v", id, r)
		}
	}
}

func TestHandleHistorySync_RevokeRemainsMarkedAfterRedelivery(t *testing.T) {
	s := newTestStoreWithLIDMap(t, nil)
	c := newHistoryTestClient(t, s)

	const (
		group = "123456789@g.us"
		bob   = "447700000002@s.whatsapp.net"
		id    = "redelivered-after-revoke"
		body  = "archived message text"
	)
	if err := s.StoreChat(group, "Project Team", time.Now()); err != nil {
		t.Fatal(err)
	}

	original := historyMsg(historyKey(group, id, false, bob), &waProto.Message{Conversation: proto.String(body)})
	runHistorySync(c, group, original)

	revoke := &waProto.Message{ProtocolMessage: &waProto.ProtocolMessage{
		Type: waProto.ProtocolMessage_REVOKE.Enum(),
		Key:  historyKey(group, id, false, bob),
	}}
	runHistorySync(c, group, historyMsg(historyKey(group, "revoke-event", false, bob), revoke))
	runHistorySync(c, group, original)

	r := mustStored(t, s, id)
	if r.content != body || !storedRevoked(t, s, id) {
		t.Fatalf("redelivered message = %+v, want original content retained and revoke marker preserved", r)
	}
	msgContext, err := s.GetMessageContext(context.Background(), id, 0, 0)
	if err != nil || msgContext.Message.Content != body || !msgContext.Message.Revoked || msgContext.Message.RevokedAt == nil {
		t.Fatalf("redelivered message context = %+v, %v; want retained content and revoked metadata", msgContext, err)
	}
}

func TestHandleHistorySync_GroupWithoutParticipant(t *testing.T) {
	s := newTestStoreWithLIDMap(t, nil)
	c := newHistoryTestClient(t, s)

	const group = "123456789@g.us"
	if err := s.StoreChat(group, "Project Team", time.Now()); err != nil {
		t.Fatalf("seed chat: %v", err)
	}

	// No participant anywhere: the rows are stored as before (the group as
	// sender), with the content unwrapped.
	runHistorySync(c, group,
		historyMsg(historyKey(group, "nosender-1", false, ""), &waProto.Message{Conversation: proto.String("orphan")}),
		historyMsg(historyKey(group, "nosender-2", false, ""), &waProto.Message{
			EphemeralMessage: &waProto.FutureProofMessage{Message: &waProto.Message{Conversation: proto.String("wrapped orphan")}},
		}),
	)

	for id, want := range map[string]string{"nosender-1": "orphan", "nosender-2": "wrapped orphan"} {
		r := mustStored(t, s, id)
		if r.chatJID != group || r.sender != "123456789" || r.content != want || r.isFromMe {
			t.Errorf("%s stored as %+v, want group-sender row with %q", id, r, want)
		}
	}
}

func TestHandleHistorySync_PollCreation(t *testing.T) {
	lunch := func() *waProto.PollCreationMessage {
		return &waProto.PollCreationMessage{
			Name: proto.String("Lunch?"),
			Options: []*waProto.PollCreationMessage_Option{
				{OptionName: proto.String("Yes")},
				{OptionName: proto.String("No")},
			},
		}
	}
	for name, m := range map[string]*waProto.Message{
		"ephemeral": {EphemeralMessage: &waProto.FutureProofMessage{Message: &waProto.Message{PollCreationMessage: lunch()}}},
		"V2":        {PollCreationMessageV2: lunch()},
		"V3":        {PollCreationMessageV3: lunch()},
		"V5":        {PollCreationMessageV5: lunch()},
		"V6":        {PollCreationMessageV6: lunch()},
	} {
		t.Run(name, func(t *testing.T) {
			s := newTestStoreWithLIDMap(t, nil)
			c := newHistoryTestClient(t, s)
			const chat = "447700000001@s.whatsapp.net"
			if err := s.StoreChat(chat, "Alice", time.Now()); err != nil {
				t.Fatalf("seed chat: %v", err)
			}

			runHistorySync(c, chat, historyMsg(historyKey(chat, "poll-1", false, ""), m))

			if r := mustStored(t, s, "poll-1"); r.content != "[poll] Lunch?" {
				t.Errorf("poll-1 content = %q, want \"[poll] Lunch?\"", r.content)
			}
			opts, err := s.GetPollOptions(context.Background(), "poll-1", chat)
			if err != nil || len(opts) != 2 || opts[0] != "Yes" || opts[1] != "No" {
				t.Errorf("poll options = %v, %v; want [Yes No]", opts, err)
			}
		})
	}
}

func TestCombinedHistoryCaptionsContactCardsAndRedaction(t *testing.T) {
	s := newTestStoreWithLIDMap(t, nil)
	c := newHistoryTestClient(t, s)
	var logs bytes.Buffer
	c.log = &stderrLogger{out: &logs, mod: "test", min: logLevelToInt["DEBUG"]}
	const chat = "447700900001@s.whatsapp.net"
	if err := s.StoreChat(chat, "Private chat name", time.Now()); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		id, want string
		message  *waProto.Message
	}{
		{"caption-combined", "Private caption", &waProto.Message{
			DocumentWithCaptionMessage: &waProto.FutureProofMessage{Message: &waProto.Message{
				DocumentMessage: &waProto.DocumentMessage{
					FileName: proto.String("Private filename.pdf"),
					Caption:  proto.String("Private caption"),
				},
			}},
		}},
		{"card-combined", "[Contact] Private contact name: +447700900002", &waProto.Message{
			EphemeralMessage: &waProto.FutureProofMessage{Message: &waProto.Message{
				ContactMessage: &waProto.ContactMessage{
					DisplayName: proto.String("Private contact name"),
					Vcard:       proto.String("BEGIN:VCARD\r\nTEL;waid=447700900002:+447700900002\r\nEND:VCARD\r\n"),
				},
			}},
		}},
	} {
		runHistorySync(c, chat, historyMsg(historyKey(chat, tc.id, false, ""), tc.message))
		result, err := s.GetMessageContext(context.Background(), tc.id, 0, 0)
		if err != nil {
			t.Fatal(err)
		}
		formatted := s.FormatMessage(context.Background(), result.Message, false)
		if result.Message.Content != tc.want || !strings.Contains(formatted, "[ID: "+tc.id+"]") || !strings.Contains(formatted, tc.want) {
			t.Fatalf("%s lost content or ID: %q", tc.id, formatted)
		}
	}
	for _, private := range []string{"Private", "447700900001", "447700900002"} {
		if strings.Contains(logs.String(), private) {
			t.Fatalf("private content in non-debug logs: %s", logs.String())
		}
	}
}

// storedRevoked reports whether the cached row is marked deleted for everyone.
func storedRevoked(t *testing.T, s *store.Store, id string) bool {
	t.Helper()
	var revoked bool
	if err := s.DB().QueryRow("SELECT revoked_at IS NOT NULL FROM messages WHERE id = ?", id).Scan(&revoked); err != nil {
		t.Fatalf("read %s: %v", id, err)
	}
	return revoked
}

const pollChat = "447700000001@s.whatsapp.net"

// newPollHistoryClient returns a client holding the message secret for
// POLL-1, a poll sent by the other side of pollChat.
func newPollHistoryClient(t *testing.T) *Client {
	t.Helper()
	c, _ := newOfflinePairedClient(t, nil)
	ctx := context.Background()
	if err := c.store.StoreChat(pollChat, "Alex", time.Now()); err != nil {
		t.Fatalf("StoreChat: %v", err)
	}
	// Message secrets belong to a saved device row.
	c.wa.Store.Account = &waAdv.ADVSignedDeviceIdentity{Details: []byte{}, AccountSignature: make([]byte, 64), AccountSignatureKey: make([]byte, 32), DeviceSignature: make([]byte, 64)}
	if err := c.wa.Store.Save(ctx); err != nil {
		t.Fatalf("save device: %v", err)
	}
	pollSender := types.NewJID("447700000001", types.DefaultUserServer)
	secret := make([]byte, 32)
	secret[0] = 1
	if err := c.wa.Store.MsgSecrets.PutMessageSecret(ctx, pollSender, pollSender, "POLL-1", secret); err != nil {
		t.Fatalf("PutMessageSecret: %v", err)
	}
	return c
}

func pollCreationMsg() *waProto.HistorySyncMsg {
	return historyMsg(historyKey(pollChat, "POLL-1", false, ""), &waProto.Message{
		PollCreationMessage: &waProto.PollCreationMessage{
			Name: proto.String("Lunch?"),
			Options: []*waProto.PollCreationMessage_Option{
				{OptionName: proto.String("Yes")},
				{OptionName: proto.String("No")},
			},
		},
	})
}

// ownPollVoteMsg is our own encrypted vote on POLL-1.
func ownPollVoteMsg(t *testing.T, c *Client, options ...string) *waProto.HistorySyncMsg {
	t.Helper()
	pollSender := types.NewJID("447700000001", types.DefaultUserServer)
	vote, err := c.wa.BuildPollVote(context.Background(), &types.MessageInfo{
		ID:            "POLL-1",
		MessageSource: types.MessageSource{Chat: pollSender, Sender: pollSender},
	}, options)
	if err != nil {
		t.Fatalf("BuildPollVote: %v", err)
	}
	return historyMsg(historyKey(pollChat, "VOTE-1", true, ""), vote)
}

func pollTally(t *testing.T, s *store.Store) map[string]int {
	t.Helper()
	_, results, err := s.GetPollResults(context.Background(), "POLL-1", pollChat)
	if err != nil {
		t.Fatalf("GetPollResults: %v", err)
	}
	got := map[string]int{}
	for _, r := range results {
		got[r.Option] = r.Votes
	}
	return got
}

func TestHandleHistorySync_ImportsPollVotes(t *testing.T) {
	t.Run("poll cached by an earlier sync", func(t *testing.T) {
		c := newPollHistoryClient(t)
		runHistorySync(c, pollChat, pollCreationMsg())
		runHistorySync(c, pollChat, ownPollVoteMsg(t, c, "Yes"))
		if got := pollTally(t, c.store); got["Yes"] != 1 || got["No"] != 0 {
			t.Errorf("poll tally = %v, want Yes=1 No=0", got)
		}
	})
	t.Run("vote ahead of its poll in one batch", func(t *testing.T) {
		c := newPollHistoryClient(t)
		// History runs newest first, so the vote precedes the poll.
		runHistorySync(c, pollChat, ownPollVoteMsg(t, c, "Yes"), pollCreationMsg())
		if got := pollTally(t, c.store); got["Yes"] != 1 || got["No"] != 0 {
			t.Errorf("poll tally = %v, want Yes=1 No=0", got)
		}
	})
}

func TestHandleHistorySync_ImportsVotesAggregatedOnPoll(t *testing.T) {
	c, _ := newOfflinePairedClient(t, nil)
	poll := pollCreationMsg()
	hashes := whatsmeow.HashPollOptions([]string{"Yes", "No"})
	poll.Message.PollUpdates = []*waProto.PollUpdate{
		{
			PollUpdateMessageKey: historyKey(pollChat, "VOTE-ME", true, ""),
			Vote:                 &waProto.PollVoteMessage{SelectedOptions: [][]byte{hashes[0]}},
			SenderTimestampMS:    proto.Int64(int64(historyTS) * 1000),
		},
		{
			PollUpdateMessageKey: historyKey(pollChat, "VOTE-ALEX", false, ""),
			Vote:                 &waProto.PollVoteMessage{SelectedOptions: [][]byte{hashes[1]}},
		},
	}
	runHistorySync(c, pollChat, poll)

	if got := pollTally(t, c.store); got["Yes"] != 1 || got["No"] != 1 {
		t.Errorf("poll tally = %v, want Yes=1 No=1", got)
	}
}

func TestRecordPollVote_KeysByAccountNotDevice(t *testing.T) {
	c, _ := newOfflinePairedClient(t, nil)
	ctx := context.Background()
	runHistorySync(c, pollChat, pollCreationMsg())
	hashes := whatsmeow.HashPollOptions([]string{"Yes", "No"})

	c.recordPollVote(ctx, "POLL-1", pollChat, "447700000001:5@s.whatsapp.net", [][]byte{hashes[0]}, time.Unix(int64(historyTS), 0), nil)
	c.recordPollVote(ctx, "POLL-1", pollChat, "447700000001@s.whatsapp.net", [][]byte{hashes[1]}, time.Unix(int64(historyTS)+1, 0), nil)

	if got := pollTally(t, c.store); got["Yes"] != 0 || got["No"] != 1 {
		t.Errorf("poll tally = %v, want Yes=0 No=1", got)
	}
}
