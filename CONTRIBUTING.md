# Contributing

Thanks for your interest in contributing!

## About this project

This repository is an MCP (Model Context Protocol) server that **wraps the [whatsmeow](https://github.com/tulir/whatsmeow) Go library** to expose a personal WhatsApp account to LLMs. The Go bridge is a thin layer over whatsmeow that adds:

- A local SQLite store for message history
- LID resolution, disappearing-message handling, and targeted history sync
- An HTTP surface consumed by the Python MCP server

When making changes, keep this framing in mind: anything related to the underlying WhatsApp protocol, session management, or media upload/download should usually be solved upstream in whatsmeow, not reimplemented here.

## Ground rules

- Open an issue before starting non-trivial work so we can agree on scope.
- One logical change per pull request. Keep diffs focused and reviewable.
- Match existing code style in both Go (`whatsapp-bridge/`) and Python (`whatsapp-mcp-server/`).
- Do not commit `.env` files, database files (`*.db`), session state, or media.

## Development workflow

1. Fork the repo and create a branch off `main`.
2. Run the Go bridge locally:

   ```bash
   cd whatsapp-bridge
   go run main.go
   ```

3. Run the Python MCP server against it (see the setup instructions in [README.md](README.md)).
4. Test against a real WhatsApp account — there is no offline fixture for whatsmeow sessions.

## Pull requests

- Describe the user-facing change and the rationale in the PR description.
- Link the issue it closes, if any.
- Call out any breaking changes to MCP tool signatures or database schema.

## Upstream considerations

Because this project wraps whatsmeow, some classes of bug belong upstream:

- Protocol-level issues (auth failures, message decryption, presence) → [whatsmeow issues](https://github.com/tulir/whatsmeow/issues)
- MCP surface, storage, or LID resolution issues → this repo

If you're unsure, open an issue here first and we'll triage.

## Licence

By contributing, you agree that your contributions will be licensed under the [MIT Licence](LICENSE).
