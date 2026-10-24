package api

import (
	"io"
	"net/http"
	"testing"

	"github.com/cli/cli/v2/internal/ghrepo"
	"github.com/cli/cli/v2/internal/workspace"
	"github.com/cli/cli/v2/pkg/httpmock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWorkspaceWriteAdapters(t *testing.T) {
	repo := &Repository{ID: "R_1", Name: "project", Owner: RepositoryOwner{Login: "acme"}, hostname: "github.com"}
	for _, tt := range []struct {
		operation, parameters, result string
		run                           func(*Client) error
	}{
		{"pull_request.create", `{"host":"github.com","repository":"acme/project","fields":{"title":"PR","body":"Body","headRefName":"chai:topic","baseRefName":"main","draft":true,"maintainerCanModify":true}}`, `{"id":"PR_1","url":"https://github.com/acme/project/pull/1"}`, func(c *Client) error {
			result, err := CreatePullRequest(c, repo, map[string]any{"title": "PR", "body": "Body", "headRefName": "chai:topic", "baseRefName": "main", "draft": true, "maintainerCanModify": true})
			if err == nil {
				assert.Equal(t, "PR_1", result.ID)
				assert.Equal(t, "https://github.com/acme/project/pull/1", result.URL)
			}
			return err
		}},
		{"issue.create", `{"host":"github.com","repository":"acme/project","fields":{"title":"Issue","body":""}}`, `{"id":"I_1","url":"https://github.com/acme/project/issues/1"}`, func(c *Client) error {
			result, err := IssueCreate(c, repo, map[string]any{"title": "Issue", "body": ""})
			if err == nil {
				assert.Equal(t, "I_1", result.ID)
			}
			return err
		}},
		{"comment.create", `{"host":"github.com","subject_id":"PR_1","body":"Comment"}`, `{"url":"https://github.com/acme/project/pull/1#issuecomment-1"}`, func(c *Client) error {
			result, err := CommentCreate(c, "github.com", CommentCreateInput{Body: "Comment", SubjectId: "PR_1"})
			if err == nil {
				assert.Equal(t, "https://github.com/acme/project/pull/1#issuecomment-1", result)
			}
			return err
		}},
		{"pull_request.review", `{"host":"github.com","repository":"acme/project","subject_id":"PR_1","fields":{"event":"COMMENT","body":"Review"}}`, `{"url":"https://github.com/acme/project/pull/1#pullrequestreview-1"}`, func(c *Client) error {
			return AddReview(c, repo, &PullRequest{ID: "PR_1"}, &PullRequestReviewInput{State: ReviewComment, Body: "Review"})
		}},
		{"repository.fork", `{"host":"github.com","repository":"acme/project","fields":null}`, `{"id":"R_fork","name":"acme_project","owner":{"login":"chai"},"createdAt":"2026-10-23T12:00:00Z"}`, func(c *Client) error {
			result, err := ForkRepo(c, repo, "", "", false)
			if err == nil {
				assert.Equal(t, "chai/acme_project", ghrepo.FullName(result))
				assert.Equal(t, "github.com", result.RepoHost())
			}
			return err
		}},
	} {
		t.Run(tt.operation, func(t *testing.T) {
			reg := &httpmock.Registry{}
			defer reg.Verify(t)
			reg.Register(httpmock.REST("GET", "_chai/capabilities"), httpmock.JSONResponse(workspace.Capabilities{Protocol: workspace.Protocol, Ready: true, Host: "github.com", Login: "bot", Generation: "g1", Operations: []string{tt.operation}}))
			reg.Register(httpmock.REST("POST", "_chai/operations/"+tt.operation), func(r *http.Request) (*http.Response, error) {
				assert.Equal(t, "api.github.com", r.URL.Host)
				assert.Equal(t, "g1", r.Header.Get("X-Chai-Workspace-Generation"))
				body, err := io.ReadAll(r.Body)
				require.NoError(t, err)
				assert.JSONEq(t, `{"parameters":`+tt.parameters+`}`, string(body))
				return httpmock.StringResponse(`{"status":"completed","result":` + tt.result + `}`)(r)
			})
			h := &http.Client{Transport: reg}
			workspace.Wrap(h, &workspace.Config{Host: "github.com"})
			require.NoError(t, tt.run(NewClientFromHTTP(h)))
			assert.Len(t, reg.Requests, 2, "typed writes must not also issue native mutations")
		})
	}
}

func TestWorkspaceWriteRejectsUnsupportedFields(t *testing.T) {
	reg := &httpmock.Registry{}
	defer reg.Verify(t)
	h := &http.Client{Transport: reg}
	workspace.Wrap(h, &workspace.Config{Host: "github.com"})
	client := NewClientFromHTTP(h)
	repo := &Repository{Name: "project", Owner: RepositoryOwner{Login: "acme"}, hostname: "github.com"}
	for _, value := range []any{true, []string{"label"}, nil, ""} {
		_, err := CreatePullRequest(client, repo, map[string]any{"unknownField": value})
		require.ErrorContains(t, err, "CHAI_GH_UNSUPPORTED")
	}
	_, err := IssueCreate(client, repo, map[string]any{"labelIds": []string{"label"}})
	require.ErrorContains(t, err, "CHAI_GH_UNSUPPORTED")
	err = AddReview(client, repo, &PullRequest{ID: "PR_1"}, &PullRequestReviewInput{State: ReviewApprove})
	require.ErrorContains(t, err, "CHAI_GH_UNSUPPORTED")
	_, err = ForkRepo(client, repo, "someone", "", false)
	require.ErrorContains(t, err, "CHAI_GH_UNSUPPORTED")
	assert.Empty(t, reg.Requests)
}
