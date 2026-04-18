#!/usr/bin/env bash
# whatsmeow API parity check.
#
# mdtest/ was removed from the whatsmeow module upstream, so instead of
# grepping mdtest we verify every whatsmeow client method we call is still
# defined as "func (cli *Client) Method(" somewhere in the module. If upstream
# renames or removes one of these, CI fails and we know to act before shipping.
set -euo pipefail

mod_version=$(go list -m -f '{{.Version}}' go.mau.fi/whatsmeow)
mod_dir=$(go env GOMODCACHE)/go.mau.fi/whatsmeow@${mod_version}

if [[ ! -d "$mod_dir" ]]; then
  echo "error: whatsmeow module not found at ${mod_dir}" >&2
  echo "  run 'go mod download' first" >&2
  exit 1
fi

methods=(
  SendMessage
  Upload
  Download
  GetGroupInfo
  BuildHistorySyncRequest
  MarkRead
  BuildReaction
  BuildEdit
  BuildRevoke
  SendChatPresence
  IsOnWhatsApp
  GetQRChannel
  Connect
  Disconnect
  Logout
  IsConnected
  CreateGroup
  LeaveGroup
  GetJoinedGroups
  UpdateGroupParticipants
  SetGroupName
  SetGroupTopic
  SetGroupAnnounce
  SetGroupLocked
  GetGroupInviteLink
  JoinGroupWithLink
  GetBlocklist
  UpdateBlocklist
  SendPresence
  GetPrivacySettings
  SetPrivacySetting
  SetStatusMessage
)

missing=()
for m in "${methods[@]}"; do
  if ! grep -rq "^func (cli \*Client) ${m}(" "$mod_dir"; then
    missing+=("$m")
  fi
done

if (( ${#missing[@]} > 0 )); then
  echo "error: whatsmeow API parity check failed." >&2
  echo "The following methods are no longer defined on *whatsmeow.Client:" >&2
  printf '  - %s\n' "${missing[@]}" >&2
  echo "Upstream renamed or removed them. Fix call sites in internal/client/." >&2
  exit 1
fi

echo "whatsmeow API parity: all ${#methods[@]} tracked methods still present at ${mod_version}."
