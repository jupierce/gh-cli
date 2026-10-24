package api

import (
	"context"
	"fmt"
	"slices"

	"github.com/cli/cli/v2/internal/ghrepo"
	"github.com/cli/cli/v2/internal/workspace"
)

func workspaceFields(params map[string]any, allowed ...string) (map[string]any, error) {
	fields := make(map[string]any)
	for key, value := range params {
		if slices.Contains(allowed, key) {
			fields[key] = value
		} else {
			return nil, workspace.Unsupported("workspace write does not support field " + key)
		}
	}
	return fields, nil
}

func workspaceRepoParameters(repo ghrepo.Interface, fields any) (map[string]any, error) {
	if repo.RepoHost() != "github.com" {
		return nil, workspace.Unsupported("workspace operations support only github.com")
	}
	return map[string]any{"host": repo.RepoHost(), "repository": ghrepo.FullName(repo), "fields": fields}, nil
}

func workspaceCreatePR(w *workspace.Client, repo *Repository, params map[string]any) (*PullRequest, error) {
	if modify, ok := params["maintainerCanModify"].(bool); ok && !modify {
		return nil, workspace.Unsupported("workspace PR creation does not support --no-maintainer-edit")
	}
	fields, err := workspaceFields(params, "title", "body", "draft", "baseRefName", "headRefName", "maintainerCanModify")
	if err != nil {
		return nil, err
	}
	p, err := workspaceRepoParameters(repo, fields)
	if err != nil {
		return nil, err
	}
	var result PullRequest
	if err := w.Operation(context.Background(), "pull_request.create", p, &result); err != nil {
		return nil, err
	}
	if result.ID == "" || result.URL == "" {
		return nil, fmt.Errorf("incomplete workspace PR result; check outcome before retrying")
	}
	return &result, nil
}

func workspaceCreateIssue(w *workspace.Client, repo *Repository, params map[string]any) (*Issue, error) {
	fields, err := workspaceFields(params, "title", "body")
	if err != nil {
		return nil, err
	}
	p, err := workspaceRepoParameters(repo, fields)
	if err != nil {
		return nil, err
	}
	var result Issue
	if err := w.Operation(context.Background(), "issue.create", p, &result); err != nil {
		return nil, err
	}
	if result.ID == "" || result.URL == "" {
		return nil, fmt.Errorf("incomplete workspace issue result; check outcome before retrying")
	}
	return &result, nil
}

func workspaceFork(w *workspace.Client, repo ghrepo.Interface, org, name string, defaultBranchOnly bool) (*Repository, error) {
	if org != "" || name != "" || defaultBranchOnly {
		return nil, workspace.Unsupported("fork owner and name are managed by the workspace coordinator; fork customization is unsupported")
	}
	p, err := workspaceRepoParameters(repo, nil)
	if err != nil {
		return nil, err
	}
	var result Repository
	if err := w.Operation(context.Background(), "repository.fork", p, &result); err != nil {
		return nil, err
	}
	result.hostname = repo.RepoHost()
	if result.ID == "" || result.Name == "" || result.Owner.Login == "" || result.CreatedAt.IsZero() || ghrepo.IsSame(repo, &result) {
		return nil, fmt.Errorf("incomplete or invalid workspace fork result; check outcome before retrying")
	}
	return &result, nil
}
