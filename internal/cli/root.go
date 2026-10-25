// Package cli wires the git-stack commands.
package cli

import (
	"github.com/spf13/cobra"
)

// Version is set at build time with -ldflags.
var Version = "dev"

// NewRoot builds the root command.
func NewRoot(app *App) *cobra.Command {
	root := &cobra.Command{
		Use:   "git-stack",
		Short: "Stacked pull requests for any Git host, gh-stack style",
		Long:  "git-stack manages stacks of branches, each with any number of commits,\nand one pull request (merge request on GitLab) per branch. Put git-stack\non your PATH and run it as `git stack`. Use `git stack -h` or\n`git stack help <command>`; git turns --help into a man page lookup.",
		// Help and usage read `git stack ...`, as git runs it.
		Annotations:   map[string]string{cobra.CommandDisplayNameAnnotation: "git stack"},
		Version:       version(),
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.PersistentFlags().StringVar(&app.Provider, "provider", "", "pull request provider: github, gitlab, none, or another installed git-stack-<name> plugin (default: git config git-stack.provider, else detected from the remote URL)")
	root.PersistentFlags().BoolVar(&app.NoVerify, "no-verify", false, "run git without hooks (pre-commit, commit-msg, pre-push, ...) for this command only; no config is changed")
	root.SetFlagErrorFunc(flagError)
	root.SetOut(app.Out)
	root.SetErr(app.Err)
	root.AddCommand(
		newInitCmd(app),
		newAddCmd(app),
		newViewCmd(app),
		newRebaseCmd(app),
		newPushCmd(app),
		newSubmitCmd(app),
		newSyncCmd(app),
		newMergeCmd(app),
		newCheckoutCmd(app),
		newSwitchCmd(app),
		newModifyCmd(app),
		newLinkCmd(app),
		newUnstackCmd(app),
		newFeedbackCmd(app),
	)
	root.AddCommand(newNavCmds(app)...)
	wrapArgs(root)
	return root
}
