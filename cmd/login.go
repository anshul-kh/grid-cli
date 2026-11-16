package cmd

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"os"

	"github.com/anshul-kh/grid-cli/internal/api"
	"github.com/anshul-kh/grid-cli/internal/auth"
	"github.com/anshul-kh/grid-cli/internal/ui"
	"github.com/anshul-kh/grid-extensions/pkg/attest"
	"github.com/spf13/cobra"
)

// loginJSON is the stable --json schema for grid login. It carries the user
// and rank — never the session key.
type loginJSON struct {
	AlreadyLoggedIn bool      `json:"alreadyLoggedIn"`
	User            *authUser `json:"user"`
	Rank            string    `json:"rank"`
}

// authUser is the user object shared by the auth commands' --json output.
// Field order here is the field order in the JSON.
type authUser struct {
	ID          string `json:"id"`
	Username    string `json:"username"`
	DisplayName string `json:"displayName"`
	AvatarURL   string `json:"avatarUrl"`
}

func newAuthUser(u auth.Credentials) *authUser {
	return &authUser{
		ID:          u.User.ID,
		Username:    u.User.Username,
		DisplayName: u.User.DisplayName,
		AvatarURL:   u.User.AvatarURL,
	}
}

var loginCmd = &cobra.Command{
	Use:   "login",
	Short: "Log in to Grid with a device code",
	Long: `Log in to Grid using a device code.

Prints a pairing code and a verification URL, opens the URL in your
browser when possible, and waits for you to approve the code. The
session is stored in your config directory with mode 0600.`,
	Args: cobra.NoArgs,
	RunE: runLogin,
}

func runLogin(cmd *cobra.Command, args []string) error {
	store, err := auth.DefaultStore()
	if err != nil {
		return err
	}
	if creds, err := store.Load(); err == nil {
		// Already logged in: idempotent success (plan Assumption 7).
		if cfg.JSON {
			return ui.WriteJSON(loginJSON{AlreadyLoggedIn: true, User: newAuthUser(creds)})
		}
		fmt.Printf("Already logged in as %s. Run `grid logout` to switch accounts.\n", creds.User.Username)
		return nil
	} else if !errors.Is(err, auth.ErrNotLoggedIn) {
		return err
	}

	// devicePublicKey (GRID_SPEC §5.1): the base64 raw 32-byte Ed25519
	// public half of this machine's device keypair — generated at first
	// login, reused on re-login, registered at device/start.
	key, err := auth.LoadOrCreateDeviceKey()
	if err != nil {
		return exitError(1, fmt.Errorf("could not prepare the device key: %w", err))
	}
	devicePublicKey := attest.PublicKeyB64(key.Public().(ed25519.PublicKey))

	client, err := newAPIClient("")
	if err != nil {
		return err
	}
	// The pairing prompt is human output: it goes to stdout, unless --json
	// is keeping stdout machine-only — then it goes to stderr.
	promptOut := os.Stdout
	if cfg.JSON {
		promptOut = os.Stderr
	}
	flow := auth.NewFlow(client, promptOut, os.Stderr,
		auth.WithBold(Theme().Bold),
		auth.WithVerbose(cfg.Verbose),
		auth.WithDevicePublicKey(devicePublicKey),
	)
	// The flow self-bounds by the server's expiresIn; each poll request is
	// bounded by the per-request timeout inside the client.
	creds, err := flow.Login(context.Background())
	if err != nil {
		return loginError(err)
	}
	if err := store.Save(creds); err != nil {
		return fmt.Errorf("could not save credentials: %w", err)
	}

	// Rank comes from the /cli/me-style data; it is best-effort here — the
	// session is already valid, so a transient failure must not fail login.
	rank := ""
	authed, err := newAPIClient(creds.SessionKey)
	if err == nil {
		ctx, cancel := commandCtx()
		defer cancel()
		if me, err := authed.Me(ctx); err == nil {
			rank = me.Rank
		}
	}

	if cfg.JSON {
		return ui.WriteJSON(loginJSON{AlreadyLoggedIn: false, User: newAuthUser(creds), Rank: rank})
	}
	theme := Theme()
	line := fmt.Sprintf("Logged in as %s", theme.Bold(creds.User.Username))
	if rank != "" {
		line += fmt.Sprintf("  ·  %s", theme.Cyan(rank))
	}
	// A bare ui.Blank, not a fresh Flow: the pairing prompt above already
	// wrote to this same os.Stdout outside any Flow's view, so a Flow
	// constructed here would wrongly think it is at the very top of the
	// stream and suppress the separating blank line this always wants.
	ui.Blank(os.Stdout)
	fmt.Fprintln(os.Stdout, line)
	return nil
}

// loginError maps the typed flow/API errors to TASK.md §3 exit codes with
// actionable messages: expired → 4 (network/server family, plan
// Assumption 13), denied → 1, network/server → 4.
func loginError(err error) error {
	switch {
	case errors.Is(err, auth.ErrLoginExpired), errors.Is(err, api.ErrAttemptExpired):
		return exitError(4, fmt.Errorf("the pairing code expired. Run `grid login` again to get a fresh code"))
	case errors.Is(err, api.ErrDenied):
		return exitError(1, fmt.Errorf("pairing was denied. Run `grid login` to start over"))
	case errors.Is(err, api.ErrServer), errors.Is(err, api.ErrNetwork), errors.Is(err, api.ErrUnexpected):
		return exitError(4, fmt.Errorf("could not reach the Grid login flow: %s. %s", err, api.UserHint(err)))
	default:
		return exitError(1, fmt.Errorf("login failed: %w", err))
	}
}

func init() {
	rootCmd.AddCommand(loginCmd)
}
