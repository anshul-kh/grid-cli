package cmd

import (
	"errors"
	"fmt"
	"os"

	"github.com/anshul-kh/grid-cli/internal/auth"
	"github.com/anshul-kh/grid-cli/internal/ui"
	"github.com/spf13/cobra"
)

// logoutJSON is the stable --json schema for grid logout.
type logoutJSON struct {
	LoggedOut     bool `json:"loggedOut"`
	ServerRevoked bool `json:"serverRevoked"`
}

var logoutCmd = &cobra.Command{
	Use:   "logout",
	Short: "Log out of Grid",
	Long: `Log out of Grid.

Revokes the session on the Grid server and deletes the local
credentials file. Logout succeeds locally even if the server cannot
be reached — the local credentials are always removed.`,
	Args: cobra.NoArgs,
	RunE: runLogout,
}

func runLogout(cmd *cobra.Command, args []string) error {
	store, err := auth.DefaultStore()
	if err != nil {
		return err
	}
	creds, err := store.Load()
	if errors.Is(err, auth.ErrNotLoggedIn) {
		if cfg.JSON {
			return ui.WriteJSON(logoutJSON{LoggedOut: false, ServerRevoked: false})
		}
		fmt.Println("Not logged in — nothing to do.")
		return nil
	} else if err != nil {
		return err
	}

	// Best-effort server revoke (DELETE /cli/session). A network or server
	// failure must not stop the local logout.
	serverRevoked := true
	client, err := newAPIClient(creds.SessionKey)
	if err != nil {
		serverRevoked = false
	} else {
		ctx, cancel := commandCtx()
		defer cancel()
		if err := client.Logout(ctx); err != nil {
			serverRevoked = false
			if cfg.Verbose {
				fmt.Fprintf(os.Stderr, "grid logout: server revoke failed: %s\n", err)
			}
		}
	}

	// The local credentials are removed no matter what the server said.
	if err := store.Delete(); err != nil {
		return fmt.Errorf("logged out on the server, but could not remove the local credentials: %w", err)
	}

	if cfg.JSON {
		return ui.WriteJSON(logoutJSON{LoggedOut: true, ServerRevoked: serverRevoked})
	}
	if !serverRevoked {
		fmt.Println("Logged out locally, but the Grid server could not revoke the session.")
		fmt.Println("If the problem persists, run `grid logout` again once the server is reachable.")
		return nil
	}
	fmt.Println("Logged out.")
	return nil
}

func init() {
	rootCmd.AddCommand(logoutCmd)
}
