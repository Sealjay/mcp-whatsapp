package client

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waMmsRetry"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

// defaultMediaRetryTimeout bounds how long a download waits for the phone to
// answer a media retry receipt. The phone has to be online to answer; an
// offline phone answers when it next comes online, which is far too late for
// a tool call.
const defaultMediaRetryTimeout = 30 * time.Second

var (
	errMediaGoneFromPhone = errors.New("the phone no longer has this media")
	errMediaRetryTimeout  = errors.New("the phone did not answer the re-upload request (it must be online)")
)

// mediaRetryCall is one outstanding media retry receipt. Concurrent downloads
// of the same message join the same call instead of sending another receipt.
type mediaRetryCall struct {
	messageID string
	mediaKey  []byte
	answer    chan *events.MediaRetry // buffered; fed by handleMediaRetry
	done      chan struct{}           // closed once directPath/err are set
	waiters   int                     // callers waiting on done (guarded by mediaRetries.mu)

	directPath string
	err        error
}

// mediaRetryRegistry tracks outstanding media retries, keyed by chat JID and
// message ID. The zero value is ready to use.
type mediaRetryRegistry struct {
	mu    sync.Mutex
	calls map[string]*mediaRetryCall
}

func mediaRetryKey(chatJID, messageID string) string {
	return chatJID + "\x00" + messageID
}

// isExpiredMediaError reports whether a download failed because the CDN no
// longer serves the stored path. History-synced messages usually carry a
// direct path whose signed `oe=` expiry has passed (403); media the CDN has
// evicted answers 404 or 410. In all three cases the phone may still hold the
// file and can re-upload it.
func isExpiredMediaError(err error) bool {
	return errors.Is(err, whatsmeow.ErrMediaDownloadFailedWith403) ||
		errors.Is(err, whatsmeow.ErrMediaDownloadFailedWith404) ||
		errors.Is(err, whatsmeow.ErrMediaDownloadFailedWith410)
}

// recoverMediaPath asks the account's own phone to re-upload the media of a
// cached message (whatsmeow's media retry receipt) and returns the new direct
// path. On success the path is also persisted, so later downloads use it.
//
// Concurrent calls for the same message share one receipt. The retry runs in
// its own goroutine, bounded by the retry timeout: a caller whose ctx ends
// returns ctx.Err() at once, while the retry still completes (and persists a
// new path) for any other caller.
func (c *Client) recoverMediaPath(ctx context.Context, messageID, chatJID string, mediaKey []byte) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	call, leader := c.joinMediaRetry(chatJID, messageID, mediaKey)
	if leader {
		go c.runMediaRetry(context.WithoutCancel(ctx), call, chatJID)
	}
	defer c.leaveMediaRetry(call)

	select {
	case <-call.done:
		return call.directPath, call.err
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

// joinMediaRetry returns the outstanding call for the message, creating and
// registering it when there is none (leader == true). Registration happens
// before the receipt is sent, so an early answer is never missed.
func (c *Client) joinMediaRetry(chatJID, messageID string, mediaKey []byte) (call *mediaRetryCall, leader bool) {
	r := &c.mediaRetries
	r.mu.Lock()
	defer r.mu.Unlock()
	key := mediaRetryKey(chatJID, messageID)
	if existing, ok := r.calls[key]; ok {
		existing.waiters++
		return existing, false
	}
	if r.calls == nil {
		r.calls = make(map[string]*mediaRetryCall)
	}
	call = &mediaRetryCall{
		messageID: messageID,
		mediaKey:  mediaKey,
		answer:    make(chan *events.MediaRetry, 1),
		done:      make(chan struct{}),
		waiters:   1,
	}
	r.calls[key] = call
	return call, true
}

func (c *Client) leaveMediaRetry(call *mediaRetryCall) {
	c.mediaRetries.mu.Lock()
	call.waiters--
	c.mediaRetries.mu.Unlock()
}

// runMediaRetry sends the receipt, waits for the phone's answer and records
// the outcome on call.
func (c *Client) runMediaRetry(ctx context.Context, call *mediaRetryCall, chatJID string) {
	ctx, cancel := context.WithTimeout(ctx, c.mediaRetryWait())
	defer cancel()

	directPath, err := c.awaitMediaRetry(ctx, call, chatJID)
	if err == nil {
		if perr := c.store.UpdateMediaDirectPath(call.messageID, chatJID, directPath); perr != nil {
			c.log.Warnf("Failed to store re-uploaded media path for message %s: %v", c.redactor.MsgID(call.messageID), perr)
		}
		c.log.Infof("Phone re-uploaded media for message %s", c.redactor.MsgID(call.messageID))
	} else {
		c.log.Warnf("Media re-upload for message %s failed: %v", c.redactor.MsgID(call.messageID), err)
	}

	r := &c.mediaRetries
	r.mu.Lock()
	key := mediaRetryKey(chatJID, call.messageID)
	if r.calls[key] == call {
		delete(r.calls, key)
	}
	call.directPath, call.err = directPath, err
	close(call.done)
	r.mu.Unlock()
}

func (c *Client) awaitMediaRetry(ctx context.Context, call *mediaRetryCall, chatJID string) (string, error) {
	info, err := c.mediaRetryMessageInfo(call.messageID, chatJID)
	if err != nil {
		return "", err
	}
	send := c.sendMediaRetry
	if send == nil {
		if c.wa == nil {
			return "", errors.New("media retry is unavailable")
		}
		send = c.wa.SendMediaRetryReceipt
	}
	c.log.Infof("Asking the phone to re-upload media for message %s", c.redactor.MsgID(call.messageID))
	if err := send(ctx, info, call.mediaKey); err != nil {
		return "", fmt.Errorf("send media retry receipt: %w", err)
	}
	select {
	case evt := <-call.answer:
		return decodeMediaRetry(evt, call.mediaKey)
	case <-ctx.Done():
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return "", errMediaRetryTimeout
		}
		return "", ctx.Err()
	}
}

func (c *Client) mediaRetryWait() time.Duration {
	if c.mediaRetryTimeout > 0 {
		return c.mediaRetryTimeout
	}
	return defaultMediaRetryTimeout
}

// mediaRetryMessageInfo rebuilds the message key fields SendMediaRetryReceipt
// reads (ID, chat, from-me, and the sender in groups) from the cached row.
func (c *Client) mediaRetryMessageInfo(messageID, chatJID string) (*types.MessageInfo, error) {
	sender, isFromMe, err := c.store.GetMessageOrigin(messageID, chatJID)
	if err != nil {
		return nil, fmt.Errorf("look up message: %w", err)
	}
	chat, err := types.ParseJID(chatJID)
	if err != nil {
		return nil, fmt.Errorf("parse chat JID: %w", err)
	}
	info := &types.MessageInfo{
		ID: messageID,
		MessageSource: types.MessageSource{
			Chat:     chat,
			IsFromMe: isFromMe,
			IsGroup:  chat.Server == types.GroupServer,
		},
	}
	if info.IsGroup {
		if sender == "" {
			return nil, errors.New("sender of the group message is unknown")
		}
		// The cache keeps only the user part of the sender, LID-resolved to
		// a phone number where possible.
		if strings.Contains(sender, "@") {
			info.Sender, err = types.ParseJID(sender)
			if err != nil {
				return nil, fmt.Errorf("parse sender JID: %w", err)
			}
		} else {
			info.Sender = types.NewJID(sender, types.DefaultUserServer)
		}
	}
	return info, nil
}

// handleMediaRetry hands the phone's answer to the waiting retry. Answers
// nobody waits for (for example after a timeout) are dropped.
func (c *Client) handleMediaRetry(evt *events.MediaRetry) {
	r := &c.mediaRetries
	r.mu.Lock()
	defer r.mu.Unlock()
	delivered := false
	for _, call := range r.calls {
		if call.messageID != evt.MessageID {
			continue
		}
		select {
		case call.answer <- evt:
		default:
		}
		delivered = true
	}
	if !delivered {
		c.log.Debugf("Ignoring media retry answer for message %s: no download is waiting", c.redactor.MsgID(evt.MessageID))
	}
}

// decodeMediaRetry decrypts the phone's answer with the message's media key
// and returns the new direct path on success.
func decodeMediaRetry(evt *events.MediaRetry, mediaKey []byte) (string, error) {
	notif, err := whatsmeow.DecryptMediaRetryNotification(evt, mediaKey)
	if errors.Is(err, whatsmeow.ErrMediaNotAvailableOnPhone) {
		return "", errMediaGoneFromPhone
	} else if err != nil {
		return "", fmt.Errorf("read the phone's answer: %w", err)
	}
	switch notif.GetResult() {
	case waMmsRetry.MediaRetryNotification_SUCCESS:
		if notif.GetDirectPath() == "" {
			return "", errors.New("the phone's answer carries no media path")
		}
		return notif.GetDirectPath(), nil
	case waMmsRetry.MediaRetryNotification_NOT_FOUND:
		return "", errMediaGoneFromPhone
	default:
		return "", fmt.Errorf("the phone could not re-upload this media (%s)", notif.GetResult())
	}
}
