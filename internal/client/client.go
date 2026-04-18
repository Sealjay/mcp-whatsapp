package client

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/mdp/qrterminal"

	"go.mau.fi/whatsmeow"
	wmstore "go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/store/sqlstore"
	waLog "go.mau.fi/whatsmeow/util/log"

	"github.com/sealjay/mcp-whatsapp/internal/security"
	"github.com/sealjay/mcp-whatsapp/internal/store"
)

// Config configures a new Client.
type Config struct {
	StoreDir         string             // directory holding messages.db and whatsapp.db
	Store            *store.Store       // initialized message store
	Logger           waLog.Logger       // optional; defaults to stderr at INFO
	AllowedMediaRoot string             // absolute path; media_path args must live under this
	Redactor         *security.Redactor // optional; defaults to a redacting instance
}

// Client wraps a whatsmeow.Client together with the message cache and logger.
type Client struct {
	wa               *whatsmeow.Client
	store            *store.Store
	log              waLog.Logger
	handlerID        uint32
	handlerInstalled bool
	allowedMediaRoot string
	redactor         *security.Redactor
}

// New constructs a Client. It sets the latest WhatsApp version best-effort,
// opens the whatsmeow sqlstore, and loads the first device. The returned
// Client is NOT connected; call Login or Connect.
func New(ctx context.Context, cfg Config) (*Client, error) {
	if cfg.Store == nil {
		return nil, errors.New("client: Config.Store is required")
	}
	if cfg.StoreDir == "" {
		return nil, errors.New("client: Config.StoreDir is required")
	}

	logger := cfg.Logger
	if logger == nil {
		logger = NewStderrLogger("Client", "INFO", true)
	}

	redactor := cfg.Redactor
	if redactor == nil {
		redactor = &security.Redactor{}
	}

	// Best-effort: fetch the latest WhatsApp version so the server doesn't
	// reject us as outdated. Failure here is non-fatal.
	if latest, err := whatsmeow.GetLatestVersion(ctx, nil); err != nil {
		logger.Warnf("Failed to get latest WhatsApp version: %v (using default)", err)
	} else if latest != nil {
		wmstore.SetWAVersion(*latest)
		logger.Infof("Using WhatsApp version: %s", latest.String())
	}

	if err := os.MkdirAll(cfg.StoreDir, 0o755); err != nil {
		return nil, fmt.Errorf("create store dir: %w", err)
	}

	dbLog := NewStderrLogger("Database", "INFO", true)
	waPath := filepath.Join(cfg.StoreDir, "whatsapp.db")
	container, err := sqlstore.New(ctx, "sqlite3", "file:"+waPath+"?_foreign_keys=on", dbLog)
	if err != nil {
		return nil, fmt.Errorf("open whatsmeow sqlstore: %w", err)
	}

	device, err := container.GetFirstDevice(ctx)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			device = container.NewDevice()
			logger.Infof("Created new device")
		} else {
			return nil, fmt.Errorf("get device: %w", err)
		}
	}

	wa := whatsmeow.NewClient(device, logger)
	if wa == nil {
		return nil, errors.New("failed to create whatsmeow client")
	}

	return &Client{
		wa:               wa,
		store:            cfg.Store,
		log:              logger,
		allowedMediaRoot: cfg.AllowedMediaRoot,
		redactor:         redactor,
	}, nil
}

// ValidateMediaPath is a bound convenience wrapper around
// security.ValidateMediaPath that uses the client's configured allowlist
// root. Callers inside internal/client and internal/mcp should prefer this
// over calling the package-level helper directly.
func (c *Client) ValidateMediaPath(userPath string) (string, error) {
	return security.ValidateMediaPath(userPath, c.allowedMediaRoot)
}

// IsLoggedIn reports whether the underlying whatsmeow device has a stored
// session. A false return means the next Connect call will emit QR pairing
// events instead of reconnecting.
func (c *Client) IsLoggedIn() bool {
	return c.wa.Store.ID != nil
}

// QRChannel exposes whatsmeow's pairing QR channel. Must be called before
// Connect when the device is not yet paired. The channel emits QRChannelItem
// events while pairing is in progress and closes on success.
func (c *Client) QRChannel(ctx context.Context) (<-chan whatsmeow.QRChannelItem, error) {
	return c.wa.GetQRChannel(ctx)
}

// Logout drops the currently paired device from WhatsApp's server and clears
// the local session. After this call, IsLoggedIn returns false and the next
// Connect will start a fresh pairing flow.
func (c *Client) Logout(ctx context.Context) error {
	return c.wa.Logout(ctx)
}

// Connect connects an already-paired device. Returns an error if not paired.
func (c *Client) Connect(ctx context.Context) error {
	if c.wa.Store.ID == nil {
		return errors.New("device not paired; run login first")
	}
	return c.wa.ConnectContext(ctx)
}

// Login runs the QR-code pairing flow. Writes the QR code to qrOut (stdout
// for the `login` subcommand). Blocks until pairing completes or ctx is done.
func (c *Client) Login(ctx context.Context, qrOut io.Writer) error {
	if c.wa.Store.ID != nil {
		// Already paired; just connect.
		return c.wa.ConnectContext(ctx)
	}

	qrChan, err := c.wa.GetQRChannel(ctx)
	if err != nil {
		return fmt.Errorf("get QR channel: %w", err)
	}
	if err := c.wa.ConnectContext(ctx); err != nil {
		return fmt.Errorf("connect: %w", err)
	}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case evt, ok := <-qrChan:
			if !ok {
				return errors.New("QR channel closed before pairing completed")
			}
			switch evt.Event {
			case "code":
				fmt.Fprintln(qrOut, "\nScan this QR code with your WhatsApp app:")
				qrterminal.GenerateHalfBlock(evt.Code, qrterminal.L, qrOut)
			case "success":
				// Give the server a moment to persist the session.
				time.Sleep(500 * time.Millisecond)
				return nil
			case "timeout":
				return errors.New("QR code scan timed out")
			default:
				// Other event types ("error", etc.) - surface the code.
				if evt.Event == "" {
					continue
				}
				c.log.Warnf("QR pairing event: %s", evt.Event)
			}
		}
	}
}

// Disconnect gracefully disconnects the underlying client.
func (c *Client) Disconnect() {
	if c.wa != nil {
		c.wa.Disconnect()
	}
}

// IsConnected reports whether the underlying whatsmeow client currently
// holds a live WebSocket to WhatsApp. Used by the daemon state machine to
// skip a second Connect call after a pairing-driven connect has already
// established the session.
func (c *Client) IsConnected() bool {
	return c.wa.IsConnected()
}

// WA exposes the underlying whatsmeow client for advanced callers.
func (c *Client) WA() *whatsmeow.Client { return c.wa }

// Store returns the message cache.
func (c *Client) Store() *store.Store { return c.store }

// Log returns the logger used by this client.
func (c *Client) Log() waLog.Logger { return c.log }
