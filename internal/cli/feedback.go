package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

func newFeedbackCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "feedback [title]",
		Short: "Open an issue for git-stack in your browser",
		Args:  cobra.ArbitraryArgs,
		RunE: func(_ *cobra.Command, args []string) error {
			u := issueURL(strings.Join(args, " "))
			fmt.Fprintln(app.Out, u)
			if err := app.openURL(u); err != nil {
				app.infof("Could not open a browser: %v", err)
			}
			return nil
		},
	}
}
