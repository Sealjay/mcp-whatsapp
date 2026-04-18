package client

import (
	"context"
	"errors"
	"fmt"

	"google.golang.org/protobuf/proto"

	"go.mau.fi/whatsmeow/proto/waE2E"
)

// SendPoll sends a poll message. options are presented in the order given.
// selectableCount is clamped to 1 when <= 0.
func (c *Client) SendPoll(ctx context.Context, recipient, question string, options []string, selectableCount int) SendResult {
	if len(options) < 2 {
		return SendResult{Success: false, Message: "poll needs at least 2 options"}
	}
	if !c.wa.IsConnected() {
		return SendResult{Success: false, Message: "Not connected to WhatsApp"}
	}
	if selectableCount <= 0 {
		selectableCount = 1
	}

	recipientJID, err := parseRecipient(recipient)
	if err != nil {
		return SendResult{Success: false, Message: err.Error()}
	}

	pollOpts := make([]*waE2E.PollCreationMessage_Option, 0, len(options))
	for _, opt := range options {
		pollOpts = append(pollOpts, &waE2E.PollCreationMessage_Option{OptionName: proto.String(opt)})
	}
	msg := &waE2E.Message{
		PollCreationMessage: &waE2E.PollCreationMessage{
			Name:                   proto.String(question),
			Options:                pollOpts,
			SelectableOptionsCount: proto.Uint32(uint32(selectableCount)),
		},
	}

	resp, err := c.wa.SendMessage(ctx, recipientJID, msg)
	if err != nil {
		return SendResult{Success: false, Message: fmt.Sprintf("Error sending poll: %v", err)}
	}

	c.persistSent(ctx, recipientJID, resp.ID, question, "", msg)

	return SendResult{
		Success: true,
		Message: fmt.Sprintf("Poll sent to %s", recipient),
		ID:      resp.ID,
	}
}

// SendContactCard sends a contact vCard. If vcardOverride is non-empty, it is
// sent verbatim; otherwise buildVCard(name, phone) synthesises a vCard 3.0.
func (c *Client) SendContactCard(ctx context.Context, recipient, name, phone, vcardOverride string) SendResult {
	if !c.wa.IsConnected() {
		return SendResult{Success: false, Message: "Not connected to WhatsApp"}
	}
	if name == "" && phone == "" && vcardOverride == "" {
		return SendResult{Success: false, Message: errors.New("contact requires name, phone, or vcard").Error()}
	}

	recipientJID, err := parseRecipient(recipient)
	if err != nil {
		return SendResult{Success: false, Message: err.Error()}
	}

	vcard := vcardOverride
	if vcard == "" {
		vcard = buildVCard(name, phone)
	}

	displayName := name
	if displayName == "" {
		displayName = phone
	}

	msg := &waE2E.Message{
		ContactMessage: &waE2E.ContactMessage{
			DisplayName: proto.String(displayName),
			Vcard:       proto.String(vcard),
		},
	}

	resp, err := c.wa.SendMessage(ctx, recipientJID, msg)
	if err != nil {
		return SendResult{Success: false, Message: fmt.Sprintf("Error sending contact card: %v", err)}
	}

	c.persistSent(ctx, recipientJID, resp.ID, displayName+" (contact card)", "", msg)

	return SendResult{
		Success: true,
		Message: fmt.Sprintf("Contact card sent to %s", recipient),
		ID:      resp.ID,
	}
}
