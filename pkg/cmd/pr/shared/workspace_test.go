package shared

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

func TestWorkspaceEditPreservesEmptyBodyAndOmittedTitle(t *testing.T) {
	for _, isPR := range []bool{false, true} {
		operation := "issue.edit"
		if isPR {
			operation = "pull_request.edit"
		}
		t.Run(operation, func(t *testing.T) {
			reg := &httpmock.Registry{}
			defer reg.Verify(t)
			reg.Register(httpmock.REST("GET", "_chai/capabilities"), httpmock.JSONResponse(workspace.Capabilities{Protocol: workspace.Protocol, Ready: true, Host: "github.com", Login: "bot", Generation: "g1", Operations: []string{operation}}))
			reg.Register(httpmock.REST("POST", "_chai/operations/"+operation), func(r *http.Request) (*http.Response, error) {
				body, err := io.ReadAll(r.Body)
				require.NoError(t, err)
				assert.JSONEq(t, `{"parameters":{"host":"github.com","repository":"acme/project","subject_id":"node-1","fields":{"body":""}}}`, string(body))
				return httpmock.StringResponse(`{"status":"completed","result":{"url":"https://github.com/acme/project/issues/1"}}`)(r)
			})
			h := &http.Client{Transport: reg}
			workspace.Wrap(h, &workspace.Config{Host: "github.com"})
			err := UpdateIssue(h, ghrepo.New("acme", "project"), "node-1", isPR, Editable{Body: EditableString{Edited: true, Value: ""}})
			require.NoError(t, err)
			assert.Len(t, reg.Requests, 2)
		})
	}
}
