package cmd

import (
	"errors"
	"fmt"
	"os"

	"github.com/anshul-kh/grid-cli/internal/api"
	"github.com/anshul-kh/grid-cli/internal/auth"
	"github.com/anshul-kh/grid-cli/internal/ui"
	"github.com/spf13/cobra"
)

var whoamiCmd = &cobra.Command{
	Use:   "whoami",
	Short: "Print the logged-in Grid account",
	Long: `Print the logged-in Grid account: username, display name, rank,
XP, and streak. Requires a prior ` + "`grid login`" + `.`,
	Args: cobra.NoArgs,
	RunE: runWhoami,
}

func runWhoami(cmd *cobra.Command, args []string) error {
	store, err := auth.DefaultStore()
	if err != nil {
		return err
	}
	creds, err := store.Load()
	if errors.Is(err, auth.ErrNotLoggedIn) {
		return exitError(3, fmt.Errorf("not logged in — run `grid login` first"))
	} else if err != nil {
		return err
	}

	client, err := newAPIClient(creds.SessionKey)
	if err != nil {
		return err
	}
	ctx, cancel := commandCtx()
	defer cancel()
	me, err := client.Me(ctx)
	if err != nil {
		return whoamiError(err)
	}

	if cfg.JSON {
		// The raw /cli/me response object (§5.5). MeResponse mirrors the
		// confirmed flat wire shape field-for-field (GRID_SPEC §5.4b), so
		// re-marshalling it reproduces the server's object exactly.
		return ui.WriteJSON(me)
	}
	theme := Theme()
	flow := ui.NewFlow(os.Stdout)
	flow.Line(theme.Bold(me.DisplayName) + " (" + me.Username + ")")
	flow.Line(fmt.Sprintf("rank %s  ·  level %d  ·  %d XP  ·  %d-day streak",
		theme.Cyan(me.Rank), me.Level, me.XP, me.CurrentStreak))
	return nil
}

// whoamiError maps /cli/me failures to TASK.md §3 exit codes: missing or
// invalid session → 3 (auth required) with the `grid login` hint; server and
// network failures → 4.
func whoamiError(err error) error {
	switch {
	case errors.Is(err, api.ErrUnauthorized):
		return exitError(3, fmt.Errorf("session missing or expired — run `grid login` to log in again"))
	case errors.Is(err, api.ErrDenied):
		return exitError(3, fmt.Errorf("this Grid account doesn't have access — run `grid login` with an authorised account"))
	case errors.Is(err, api.ErrServer), errors.Is(err, api.ErrNetwork), errors.Is(err, api.ErrUnexpected):
		return exitError(4, fmt.Errorf("could not fetch your Grid account: %s. %s", err, api.UserHint(err)))
	default:
		return exitError(4, fmt.Errorf("could not fetch your Grid account: %w", err))
	}
}

func init() {
	rootCmd.AddCommand(whoamiCmd)
}
