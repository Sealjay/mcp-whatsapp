# Contributing

## About this project

This repository wraps the [whatsmeow](https://github.com/tulir/whatsmeow) Go library to expose a personal WhatsApp account through an HTTP MCP server. The Go application adds a local SQLite message cache, LID resolution, history sync, and MCP tools.

Protocol, session-management, and media-transfer issues usually belong in whatsmeow. The MCP server, cache, and LID-resolution code belong here.

## Before you start

- Open an issue before substantial work so the scope can be agreed.
- Keep each pull request focused on one logical change.
- Match the existing Go style in `cmd/` and `internal/`.
- Do not commit `.env` files, databases (`*.db`), session state, or media.
- Start from `main` on a `feat/<slug>` or `fix-<issue>` branch. Use a worktree under `.claude/worktrees/` when following the repository workflow.

## Build and test

```bash
make build        # writes ./bin/whatsapp-mcp
make lint         # go vet and verify gofmt
make test         # unit tests
make test-race    # race detector
make e2e          # build and run the HTTP MCP end-to-end tests
make smoke        # boot-test without connecting to WhatsApp
```

Run the checks relevant to your change. The unit tests use local fixtures and do not need a WhatsApp account. For manual integration testing, pair a device with:

```bash
./bin/whatsapp-mcp login
```

`make upgrade-check` updates the whatsmeow dependency to `@main`, tidies modules, builds, and tests. It changes `go.mod` and `go.sum`; use it when working on an intentional dependency update and review those changes before committing.

## Pull requests

- Open pull requests against `main`; the repository does not use a `dev` branch.
- Describe the user-facing change and its rationale.
- Link the issue it closes, if any.
- Call out breaking changes to MCP tool signatures or the database schema.
- Squash-merge after the required checks pass.

## Upstream issues

- Protocol issues such as authentication failures, message decryption, or presence: [whatsmeow issues](https://github.com/tulir/whatsmeow/issues).
- MCP, storage, or LID-resolution issues: this repository.

If you are unsure where an issue belongs, open an issue here for triage.

## Licence

By contributing, you agree that your contributions will be licensed under the [MIT Licence](LICENSE).
