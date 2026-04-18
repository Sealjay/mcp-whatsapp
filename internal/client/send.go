package client

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"google.golang.org/protobuf/proto"

	"go.mau.fi/whatsmeow"
	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/types"

	"github.com/sealjay/mcp-whatsapp/internal/media"
	"github.com/sealjay/mcp-whatsapp/internal/store"
)

// SendResult is the public return value from Send/SendMedia.
type SendResult struct {
	Success bool
	Message string
	ID      string
}

// SendMediaOptions bundles the inputs to SendMediaWithOptions so callers can
// add per-message flags (view_once, etc.) without growing the argument list.
type SendMediaOptions struct {
	Recipient string
	Caption   string
	MediaPath string
	ViewOnce  bool
}

// Send sends a text message.
func (c *Client) Send(ctx context.Context, recipient, message string) SendResult {
	return c.send(ctx, recipient, message, "", false)
}

// SendMedia uploads mediaPath and sends it with optional caption.
func (c *Client) SendMedia(ctx context.Context, recipient, caption, mediaPath string) SendResult {
	return c.SendMediaWithOptions(ctx, SendMediaOptions{
		Recipient: recipient,
		Caption:   caption,
		MediaPath: mediaPath,
	})
}

// SendMediaWithOptions is the extensible entry point for media sends.
func (c *Client) SendMediaWithOptions(ctx context.Context, opts SendMediaOptions) SendResult {
	return c.send(ctx, opts.Recipient, opts.Caption, opts.MediaPath, opts.ViewOnce)
}

// send is the unified implementation shared by Send and SendMedia.
func (c *Client) send(ctx context.Context, recipient, message, mediaPath string, viewOnce bool) SendResult {
	if !c.wa.IsConnected() {
		return SendResult{Success: false, Message: "Not connected to WhatsApp"}
	}

	recipientJID, err := parseRecipient(recipient)
	if err != nil {
		return SendResult{Success: false, Message: err.Error()}
	}

	// Detect disappearing timer for group chats; direct chats default to 0.
	var ephemeralExpiration uint32
	if recipientJID.Server == "g.us" {
		if gi, err := c.wa.GetGroupInfo(ctx, recipientJID); err == nil && gi.DisappearingTimer > 0 {
			ephemeralExpiration = gi.DisappearingTimer
			c.log.Infof("Auto-detected group disappearing timer: %d seconds", ephemeralExpiration)
		}
	}

	msg := &waProto.Message{}
	if ephemeralExpiration > 0 {
		msg.MessageContextInfo = &waProto.MessageContextInfo{
			MessageAddOnDurationInSecs: proto.Uint32(ephemeralExpiration),
		}
	}

	if mediaPath != "" {
		if err := c.attachMedia(ctx, msg, mediaPath, message, viewOnce); err != nil {
			return SendResult{Success: false, Message: err.Error()}
		}
	} else {
		msg.Conversation = proto.String(message)
	}

	resp, err := c.wa.SendMessage(ctx, recipientJID, msg)
	if err != nil {
		return SendResult{Success: false, Message: fmt.Sprintf("Error sending message: %v", err)}
	}

	// Cache the sent message so local history stays complete.
	c.persistSent(ctx, recipientJID, resp.ID, message, mediaPath, msg)

	return SendResult{
		Success: true,
		Message: fmt.Sprintf("Message sent to %s", recipient),
		ID:      resp.ID,
	}
}

// parseRecipient normalizes a phone number or JID string into types.JID.
func parseRecipient(recipient string) (types.JID, error) {
	if strings.Contains(recipient, "@") {
		jid, err := types.ParseJID(recipient)
		if err != nil {
			return types.JID{}, fmt.Errorf("Error parsing JID: %v", err)
		}
		return jid, nil
	}
	return types.JID{User: recipient, Server: types.DefaultUserServer}, nil
}

// attachMedia uploads mediaPath and populates the appropriate message field.
// When viewOnce is true, the resulting Image/Video/Audio submessage is flagged
// as view-once. DocumentMessage has no view-once support in WhatsApp clients,
// so the flag is silently ignored for documents.
func (c *Client) attachMedia(ctx context.Context, msg *waProto.Message, mediaPath, caption string, viewOnce bool) error {
	safePath, err := c.ValidateMediaPath(mediaPath)
	if err != nil {
		return fmt.Errorf("media_path rejected: %w", err)
	}
	mediaData, err := os.ReadFile(safePath)
	if err != nil {
		return fmt.Errorf("read media file: %w", err)
	}

	mediaType, mimeType := mediaTypeFromExt(safePath)

	resp, err := c.wa.Upload(ctx, mediaData, mediaType)
	if err != nil {
		return fmt.Errorf("Error uploading media: %v", err)
	}
	c.log.Infof("Media uploaded: url=%s bytes=%d", c.redactor.URL(resp.URL), resp.FileLength)

	switch mediaType {
	case whatsmeow.MediaImage:
		msg.ImageMessage = &waProto.ImageMessage{
			Caption:       proto.String(caption),
			Mimetype:      proto.String(mimeType),
			URL:           &resp.URL,
			DirectPath:    &resp.DirectPath,
			MediaKey:      resp.MediaKey,
			FileEncSHA256: resp.FileEncSHA256,
			FileSHA256:    resp.FileSHA256,
			FileLength:    &resp.FileLength,
		}
		if viewOnce {
			msg.ImageMessage.ViewOnce = proto.Bool(true)
		}
	case whatsmeow.MediaAudio:
		var seconds uint32 = 30
		var waveform []byte
		if strings.Contains(mimeType, "ogg") {
			s, w, aerr := media.AnalyzeOggOpus(mediaData)
			if aerr != nil {
				return fmt.Errorf("Failed to analyze Ogg Opus file: %v", aerr)
			}
			seconds = s
			waveform = w
		} else {
			c.log.Warnf("Not an Ogg Opus file: %s", mimeType)
		}
		msg.AudioMessage = &waProto.AudioMessage{
			Mimetype:      proto.String(mimeType),
			URL:           &resp.URL,
			DirectPath:    &resp.DirectPath,
			MediaKey:      resp.MediaKey,
			FileEncSHA256: resp.FileEncSHA256,
			FileSHA256:    resp.FileSHA256,
			FileLength:    &resp.FileLength,
			Seconds:       proto.Uint32(seconds),
			PTT:           proto.Bool(true),
			Waveform:      waveform,
		}
		if viewOnce {
			msg.AudioMessage.ViewOnce = proto.Bool(true)
		}
	case whatsmeow.MediaVideo:
		msg.VideoMessage = &waProto.VideoMessage{
			Caption:       proto.String(caption),
			Mimetype:      proto.String(mimeType),
			URL:           &resp.URL,
			DirectPath:    &resp.DirectPath,
			MediaKey:      resp.MediaKey,
			FileEncSHA256: resp.FileEncSHA256,
			FileSHA256:    resp.FileSHA256,
			FileLength:    &resp.FileLength,
		}
		if viewOnce {
			msg.VideoMessage.ViewOnce = proto.Bool(true)
		}
	case whatsmeow.MediaDocument:
		// View-once on documents is not supported by WhatsApp clients; silently ignored.
		msg.DocumentMessage = &waProto.DocumentMessage{
			Title:         proto.String(filepath.Base(mediaPath)),
			Caption:       proto.String(caption),
			Mimetype:      proto.String(mimeType),
			URL:           &resp.URL,
			DirectPath:    &resp.DirectPath,
			MediaKey:      resp.MediaKey,
			FileEncSHA256: resp.FileEncSHA256,
			FileSHA256:    resp.FileSHA256,
			FileLength:    &resp.FileLength,
		}
	}
	return nil
}

// mediaTypeFromExt maps a file extension to a whatsmeow MediaType + mime type.
func mediaTypeFromExt(path string) (whatsmeow.MediaType, string) {
	ext := ""
	if idx := strings.LastIndex(path, "."); idx >= 0 {
		ext = strings.ToLower(path[idx+1:])
	}
	switch ext {
	case "jpg", "jpeg":
		return whatsmeow.MediaImage, "image/jpeg"
	case "png":
		return whatsmeow.MediaImage, "image/png"
	case "gif":
		return whatsmeow.MediaImage, "image/gif"
	case "webp":
		return whatsmeow.MediaImage, "image/webp"
	case "ogg":
		return whatsmeow.MediaAudio, "audio/ogg; codecs=opus"
	case "mp4":
		return whatsmeow.MediaVideo, "video/mp4"
	case "avi":
		return whatsmeow.MediaVideo, "video/avi"
	case "mov":
		return whatsmeow.MediaVideo, "video/quicktime"
	default:
		return whatsmeow.MediaDocument, "application/octet-stream"
	}
}

// persistSent writes a cache row for a message we sent so the local history
// stays complete regardless of device multiplexing.
func (c *Client) persistSent(ctx context.Context, recipientJID types.JID, msgID, message, mediaPath string, msg *waProto.Message) {
	chatJID := recipientJID.String()
	if !strings.HasSuffix(chatJID, "@g.us") {
		chatJID = c.store.ResolveLIDToJID(chatJID)
	}

	var chatName string
	if recipientJID.Server == "g.us" {
		if gi, err := c.wa.GetGroupInfo(ctx, recipientJID); err == nil && gi.Name != "" {
			chatName = gi.Name
		} else {
			chatName = "Group " + recipientJID.User
		}
	} else {
		if contact, err := c.wa.Store.Contacts.GetContact(ctx, recipientJID); err == nil && contact.FullName != "" {
			chatName = contact.FullName
		} else {
			chatName = recipientJID.User
		}
	}

	now := time.Now()
	if err := c.store.StoreChat(chatJID, chatName, now); err != nil {
		c.log.Warnf("Failed to store chat for sent message: %v", err)
	}

	ourJID := ""
	if c.wa.Store.ID != nil {
		ourJID = c.wa.Store.ID.User
	}

	var (
		storedMediaType, storedFilename, storedURL      string
		storedMediaKey, storedFileSHA256, storedFileEnc []byte
		storedFileLength                                uint64
	)
	if mediaPath != "" {
		storedMediaType, storedFilename, storedURL,
			storedMediaKey, storedFileSHA256, storedFileEnc, storedFileLength = extractMediaInfo(msg)
	}

	err := c.store.StoreMessage(ctx, store.Message{
		ID:        msgID,
		ChatJID:   chatJID,
		Sender:    ourJID,
		Content:   message,
		Timestamp: now,
		IsFromMe:  true,
		MediaType: storedMediaType,
		Filename:  storedFilename,
		URL:       storedURL,
	}, storedMediaKey, storedFileSHA256, storedFileEnc, storedFileLength)
	if err != nil {
		c.log.Warnf("Failed to store sent message: %v", err)
		return
	}
	c.log.Infof("[%s] -> %s: %s", now.Format("2006-01-02 15:04:05"), c.redactor.JID(chatJID), c.redactor.Body(message))
}
