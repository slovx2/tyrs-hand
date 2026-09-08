package workerprotocol

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestWorkspaceOnlyExplicitNullClearsBinding(t *testing.T) {
	for _, test := range []struct {
		name, body       string
		wantError, bound bool
	}{
		{name: "明确未绑定", body: `{"workspace":null}`},
		{name: "有效绑定", body: `{"workspace":{"workspaceId":"11111111-1111-4111-8111-111111111111","forums":[]}}`, bound: true},
		{name: "遗漏字段", body: `{}`, wantError: true},
		{name: "空身份", body: `{"workspace":{}}`, wantError: true},
		{name: "格式错误", body: `{"workspace":[]}`, wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				require.Equal(t, "/worker/v1/workspace", r.URL.Path)
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(test.body))
			}))
			defer endpoint.Close()
			manifest, err := NewClient(endpoint.URL, "test", time.Second).Workspace(context.Background())
			if test.wantError {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, test.bound, manifest != nil)
		})
	}
}
