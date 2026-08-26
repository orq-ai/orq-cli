package commands

import (
	"errors"
	"fmt"
	"unicode/utf8"

	"orq/cli/custom/auth"

	bartolocli "github.com/orq-ai/bartolo/cli"
	"github.com/spf13/cobra"
)

type workspaceRow struct {
	ID           string `json:"id"`
	Key          string `json:"key"`
	Name         string `json:"name"`
	TotalMembers int    `json:"total_members"`
	Active       bool   `json:"active"`
}

func NewWorkspaceCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "workspace",
		Short: "Manage CLI workspace context",
	}
	cmd.AddCommand(newWorkspaceListCommand())
	cmd.AddCommand(newWorkspaceUseCommand())
	return cmd
}

func newWorkspaceListCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List available workspaces",
		RunE: func(cmd *cobra.Command, args []string) error {
			session, err := auth.ReadSession()
			if err != nil {
				return err
			}
			if session == nil {
				return errors.New("you are not logged in")
			}
			client := auth.NewClient(sessionAPIBase(session)).WithContext(cmd.Context())
			session, err = client.WhoAmI()
			if err != nil {
				return err
			}
			activeKey := ""
			if session.ActiveWorkspaceKey != nil {
				activeKey = *session.ActiveWorkspaceKey
			}
			rows := make([]workspaceRow, 0, len(session.Workspaces))
			nameWidth := 0
			for _, w := range session.Workspaces {
				ws := workspaceFromMap(w)
				rows = append(rows, workspaceRow{
					ID:           ws.ID,
					Key:          ws.Key,
					Name:         ws.Name,
					TotalMembers: ws.TotalMembers,
					Active:       ws.Key == activeKey,
				})
				if n := utf8.RuneCountInString(ws.Name); n > nameWidth {
					nameWidth = n
				}
			}
			if wantsHumanView(cmd) {
				printWorkspaceList(rows, nameWidth)
				return nil
			}
			return emit(map[string]any{
				"active_workspace_key": session.ActiveWorkspaceKey,
				"workspaces":           rows,
			})
		},
	}
	return cmd
}

func newWorkspaceUseCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "use [key]",
		Short: "Switch the active workspace",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			session, err := auth.ReadSession()
			if err != nil {
				return err
			}
			if session == nil {
				return errors.New("you are not logged in")
			}
			client := auth.NewClient(sessionAPIBase(session)).WithContext(cmd.Context())
			session, err = client.WhoAmI()
			if err != nil {
				return err
			}
			workspaceKey := ""
			switch {
			case len(args) > 0:
				workspaceKey = args[0]
			case hasInteractiveTTY():
				// No argument at a terminal means "let me choose". Do NOT
				// pre-fill the active workspace here: that made the picker
				// unreachable for anyone who already had one active, so `use`
				// with no argument silently re-selected the current workspace
				// instead of switching.
				workspaceKey, err = selectWorkspace(session.Workspaces, "Choose the workspace to activate")
				if err != nil {
					return err
				}
			case session.ActiveWorkspaceKey != nil:
				// Non-interactive with no argument: re-assert the active
				// workspace rather than fail, so scripts stay deterministic.
				workspaceKey = *session.ActiveWorkspaceKey
			}
			if workspaceKey == "" {
				return errors.New("no workspace is available for this user")
			}
			session, err = client.UseWorkspace(workspaceKey)
			if err != nil {
				return err
			}
			report := BuildIdentityReport(session, &client.URLs)
			activeName := workspaceKey
			for _, w := range report.Workspaces {
				if w.Key == workspaceKey {
					activeName = w.Name
					break
				}
			}
			// Snapshotted before PreRun's session-token injection; reading the
			// env here would always see our own injected key and cry wolf.
			shadowed := explicitAPIKey
			if wantsHumanView(cmd) {
				success("Active workspace: %s (%s)", activeName, workspaceKey)
				if shadowed {
					Warn("an explicit API key takes precedence, so this switch will not affect API calls until it is unset")
				}
				return nil
			}
			if shadowed {
				Warn("an explicit API key (ORQ_API_KEY or a credentials profile) is configured and takes precedence over the session, so this workspace switch will not affect API calls until the key is unset")
			}
			return emit(report)
		},
	}
	return cmd
}

// printWorkspaceList renders the workspace roster as an aligned table: a dim
// header row, a green dot on the active workspace, and right-aligned member
// counts. Column widths grow to fit the data.
func printWorkspaceList(rows []workspaceRow, nameWidth int) {
	const memHdr = "MEMBERS"
	keyWidth := len("KEY")
	for _, r := range rows {
		if n := utf8.RuneCountInString(r.Key); n > keyWidth {
			keyWidth = n
		}
	}
	if nameWidth < len("NAME") {
		nameWidth = len("NAME")
	}
	out := bartolocli.Stdout
	heading("Workspaces")
	// Header row. The 2-space gutter lines up with the active-marker column.
	fmt.Fprintf(out, "  %s%s  %s  %s\n",
		"  ",
		paint(ansiDim, pad("NAME", nameWidth)),
		paint(ansiDim, pad("KEY", keyWidth)),
		paint(ansiDim, memHdr))
	anyActive := false
	for _, r := range rows {
		marker := "  "
		if r.Active {
			marker = paint(ansiOK, "● ")
			anyActive = true
		}
		members := fmt.Sprintf("%*d", len(memHdr), r.TotalMembers)
		fmt.Fprintf(out, "  %s%s  %s  %s\n",
			marker,
			pad(r.Name, nameWidth),
			pad(r.Key, keyWidth),
			paint(ansiDim, members))
	}
	if anyActive {
		fmt.Fprintln(out, paint(ansiDim, "\n● active"))
	}
}
