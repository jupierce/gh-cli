package root

import (
	"slices"
	"strings"

	"github.com/cli/cli/v2/internal/workspace"
	"github.com/cli/cli/v2/pkg/cmdutil"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// This is a compatibility manifest, not an access boundary. The manager must
// authorize every request independently of the binary and its claimed command.
func workspacePreRun(cmd *cobra.Command, client *workspace.Client) error {
	name := strings.TrimPrefix(cmd.CommandPath(), cmd.Root().Name()+" ")
	var operation string
	var flags []string
	switch name {
	case "version", "completion", "auth status":
		return nil
	case "auth token":
		return workspace.Unsupported("GitHub credentials remain in the workspace manager; no local token is available")
	case "api", "pr view", "pr list", "pr status", "pr diff", "pr checks", "pr checkout", "issue view", "issue list", "issue status", "repo view", "repo clone", "repo set-default", "search repos", "search issues", "search prs", "search code", "search commits":
		if cmd.Flags().Changed("cache") {
			return workspace.Unsupported("workspace response caching belongs to the manager; --cache is unsupported")
		}
		if cmd.Flags().Changed("web") {
			return workspace.Unsupported("--web is outside the workspace command contract")
		}
		return nil
	case "pr create":
		operation = "pull_request.create"
		flags = []string{"title", "body", "body-file", "base", "head", "draft"}
		for _, flag := range []string{"head", "base", "title"} {
			value, _ := cmd.Flags().GetString(flag)
			if value == "" {
				return workspace.Unsupported("workspace PR creation requires explicit --head, --base and --title; push the head branch through the existing Git workflow first")
			}
		}
	case "issue create":
		operation = "issue.create"
		flags = []string{"title", "body", "body-file"}
		if title, _ := cmd.Flags().GetString("title"); title == "" {
			return workspace.Unsupported("workspace issue creation requires --title")
		}
	case "pr comment", "issue comment":
		operation = "comment.create"
		flags = []string{"body", "body-file"}
	case "pr review":
		operation = "pull_request.review"
		flags = []string{"comment", "body", "body-file"}
		if comment, _ := cmd.Flags().GetBool("comment"); !comment {
			return workspace.Unsupported("workspace reviews support only --comment")
		}
	case "pr edit":
		operation = "pull_request.edit"
		flags = []string{"title", "body", "body-file"}
	case "issue edit":
		operation = "issue.edit"
		flags = []string{"title", "body", "body-file"}
	case "repo fork":
		operation = "repository.fork"
		flags = []string{"clone", "remote", "remote-name"}
	default:
		return workspace.Unsupported(name + " is outside the workspace command contract")
	}
	var unsupported error
	cmd.Flags().Visit(func(flag *pflag.Flag) {
		if flag.Name != "repo" && !slices.Contains(flags, flag.Name) {
			unsupported = workspace.Unsupported("--" + flag.Name + " is not supported for workspace " + name)
		}
	})
	if unsupported != nil {
		return unsupported
	}
	if name == "pr edit" || name == "issue edit" {
		if !cmd.Flags().Changed("title") && !cmd.Flags().Changed("body") && !cmd.Flags().Changed("body-file") && !cmd.Flags().Changed("base") {
			return workspace.Unsupported("workspace edits require explicit fields")
		}
	} else if name != "repo fork" && !cmd.Flags().Changed("body") && !cmd.Flags().Changed("body-file") {
		return workspace.Unsupported("workspace writes require --body or --body-file")
	}
	caps, err := client.Capabilities(cmd.Context())
	if err != nil {
		return err
	}
	if !slices.Contains(caps.Operations, operation) {
		return workspace.Unsupported("manager does not advertise " + operation)
	}
	return nil
}

// Some command families override Cobra's inherited persistent hook. Apply the
// workspace compatibility check before those hooks can start their own flows.
func guardWorkspaceHooks(cmd *cobra.Command, f *cmdutil.Factory) {
	for _, child := range cmd.Commands() {
		if child.PersistentPreRunE != nil || child.PersistentPreRun != nil {
			originalE, original := child.PersistentPreRunE, child.PersistentPreRun
			child.PersistentPreRun = nil
			child.PersistentPreRunE = func(invoked *cobra.Command, args []string) error {
				if f.Workspace != nil {
					client, err := f.Workspace()
					if err != nil {
						return err
					}
					if client != nil {
						if err := workspacePreRun(invoked, client); err != nil {
							return err
						}
					}
				}
				if originalE != nil {
					return originalE(invoked, args)
				}
				if original != nil {
					original(invoked, args)
				}
				return nil
			}
		}
		guardWorkspaceHooks(child, f)
	}
}
