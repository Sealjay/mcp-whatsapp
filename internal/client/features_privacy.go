package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"go.mau.fi/whatsmeow/types"
)

// privacySettingTypes is the ordered list of valid PrivacySettingType values.
// Order is preserved in validation error messages for deterministic output.
var privacySettingTypes = []types.PrivacySettingType{
	types.PrivacySettingTypeGroupAdd,
	types.PrivacySettingTypeLastSeen,
	types.PrivacySettingTypeStatus,
	types.PrivacySettingTypeProfile,
	types.PrivacySettingTypeReadReceipts,
	types.PrivacySettingTypeOnline,
	types.PrivacySettingTypeCallAdd,
	types.PrivacySettingTypeMessages,
	types.PrivacySettingTypeDefense,
	types.PrivacySettingTypeStickers,
}

// privacySettingValues is the ordered list of valid PrivacySetting values.
var privacySettingValues = []types.PrivacySetting{
	types.PrivacySettingAll,
	types.PrivacySettingContacts,
	types.PrivacySettingContactAllowlist,
	types.PrivacySettingContactBlacklist,
	types.PrivacySettingMatchLastSeen,
	types.PrivacySettingKnown,
	types.PrivacySettingNone,
	types.PrivacySettingOnStandard,
	types.PrivacySettingOff,
}

func joinPrivacyNames() string {
	parts := make([]string, len(privacySettingTypes))
	for i, n := range privacySettingTypes {
		parts[i] = string(n)
	}
	return strings.Join(parts, ", ")
}

func joinPrivacyValues() string {
	parts := make([]string, len(privacySettingValues))
	for i, v := range privacySettingValues {
		parts[i] = string(v)
	}
	return strings.Join(parts, ", ")
}

// parsePresence validates the public presence state string and maps it to a
// whatsmeow types.Presence. Exposed separately so tests can exercise the
// validation branch without a connected client.
func parsePresence(state string) (types.Presence, error) {
	switch state {
	case string(types.PresenceAvailable):
		return types.PresenceAvailable, nil
	case string(types.PresenceUnavailable):
		return types.PresenceUnavailable, nil
	default:
		return "", fmt.Errorf("invalid state %q: must be one of available, unavailable", state)
	}
}

// SendPresence sets the user's own online availability. state must be
// "available" or "unavailable".
func (c *Client) SendPresence(ctx context.Context, state string) error {
	p, err := parsePresence(state)
	if err != nil {
		return err
	}
	if !c.wa.IsConnected() {
		return errors.New("not connected to WhatsApp")
	}
	if err := c.wa.SendPresence(ctx, p); err != nil {
		return fmt.Errorf("send presence: %w", err)
	}
	return nil
}

// GetPrivacySettings returns the current privacy settings as JSON.
func (c *Client) GetPrivacySettings(ctx context.Context) (string, error) {
	if !c.wa.IsConnected() {
		return "", errors.New("not connected to WhatsApp")
	}
	settings := c.wa.GetPrivacySettings(ctx)
	b, err := json.Marshal(settings)
	if err != nil {
		return "", fmt.Errorf("marshal privacy settings: %w", err)
	}
	return string(b), nil
}

// validatePrivacySettingName returns an error whose message lists the allowed
// names if the given name is not one of whatsmeow's known constants.
func validatePrivacySettingName(name string) error {
	for _, n := range privacySettingTypes {
		if string(n) == name {
			return nil
		}
	}
	return fmt.Errorf("invalid name %q: must be one of %s", name, joinPrivacyNames())
}

// validatePrivacySettingValue returns an error whose message lists the allowed
// values if the given value is not one of whatsmeow's known constants.
func validatePrivacySettingValue(value string) error {
	for _, v := range privacySettingValues {
		if string(v) == value {
			return nil
		}
	}
	return fmt.Errorf("invalid value %q: must be one of %s", value, joinPrivacyValues())
}

// SetPrivacySetting changes a single privacy setting. Returns the resulting
// privacy settings as JSON. name and value must be one of whatsmeow's known
// constants; invalid input is rejected with a message listing the allowed
// values for the offending argument.
func (c *Client) SetPrivacySetting(ctx context.Context, name, value string) (string, error) {
	if err := validatePrivacySettingName(name); err != nil {
		return "", err
	}
	if err := validatePrivacySettingValue(value); err != nil {
		return "", err
	}
	if !c.wa.IsConnected() {
		return "", errors.New("not connected to WhatsApp")
	}
	settings, err := c.wa.SetPrivacySetting(ctx, types.PrivacySettingType(name), types.PrivacySetting(value))
	if err != nil {
		return "", fmt.Errorf("set privacy setting: %w", err)
	}
	b, err := json.Marshal(settings)
	if err != nil {
		return "", fmt.Errorf("marshal privacy settings: %w", err)
	}
	return string(b), nil
}

// SetStatusMessage updates the user's "About" text.
func (c *Client) SetStatusMessage(ctx context.Context, text string) error {
	if !c.wa.IsConnected() {
		return errors.New("not connected to WhatsApp")
	}
	if err := c.wa.SetStatusMessage(ctx, text); err != nil {
		return fmt.Errorf("set status message: %w", err)
	}
	return nil
}
