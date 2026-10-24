package factory

import (
	"testing"

	"github.com/cli/cli/v2/git"
	"github.com/cli/cli/v2/internal/config"
	"github.com/cli/cli/v2/internal/gh"
	"github.com/cli/cli/v2/internal/workspace"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWorkspaceRemoteIdentityPreservesGitURLs(t *testing.T) {
	t.Setenv("GH_HOST", "github.com")
	upstream := git.NewRemote("upstream", "http://localhost:8080/github.com/acme/project.git")
	origin := git.NewRemote("origin", "http://localhost:8080/github.com/chai/project.git")
	origin.PushURL = git.NewRemote("push", "http://localhost:8080/github.com/chai/push-target.git").FetchURL
	rr := remoteResolver{
		readRemotes: func() (git.RemoteSet, error) {
			return git.RemoteSet{origin, upstream, git.NewRemote("other", "http://localhost:9999/github.com/acme/project.git")}, nil
		},
		getConfig: func() (gh.Config, error) { return config.NewMockConfig(), nil },
		getWorkspace: func() (*workspace.Config, error) {
			return &workspace.Config{Host: "github.com", GitProxyPrefixes: []string{"http://localhost:8080/github.com/"}}, nil
		},
		urlTranslator: identityTranslator{},
	}
	remotes, err := rr.Resolver()()
	require.NoError(t, err)
	require.Len(t, remotes, 2)
	assert.Equal(t, "upstream", remotes[0].Name)
	assert.Equal(t, "github.com", remotes[0].RepoHost())
	assert.Equal(t, "acme", remotes[0].RepoOwner())
	assert.Equal(t, "project", remotes[0].RepoName())
	assert.Same(t, upstream, remotes[0].Remote)
	assert.Same(t, origin, remotes[1].Remote)
	assert.Equal(t, "http://localhost:8080/github.com/chai/push-target.git", remotes[1].PushURL.String())
	assert.Equal(t, "http://localhost:8080/github.com/acme/project.git", upstream.FetchURL.String())
}
