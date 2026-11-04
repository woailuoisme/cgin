package console_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"cgin/internal/bootstrap"
	"cgin/internal/console"
)

func TestDITreeCommand_Run(t *testing.T) {
	t.Parallel()

	out, err := execWithTestFactory(context.Background(), &testFactory{
		InjectorValue: bootstrap.NewInjector(),
	}, "di:tree")
	require.NoError(t, err)

	assert.Contains(t, out, "Dependency Injection Container")
	assert.Contains(t, out, "cgin/internal/config.Config")
	assert.Contains(t, out, "Total services registered:")
}

func TestRouteListCommand_Basic(t *testing.T) {
	out, err := execWithTestFactory(context.Background(), nil, "route:list")
	require.NoError(t, err)
	assert.Contains(t, out, "HTTP Route List")
	assert.Contains(t, out, "/livez")
	assert.Contains(t, out, "/readyz")
	assert.Contains(t, out, "handler.SystemHandler.Liveness")
	assert.Contains(t, out, "handler.SystemHandler.Index")
	assert.Contains(t, out, "Total:")
}

func TestRouteListCommand_DeprecatedRejected(t *testing.T) {
	_, err := execWithTestFactory(context.Background(), nil, "route", "list")
	require.Error(t, err)
	assert.Contains(t, err.Error(), `unknown command "route"`)
}

func TestRouteListCommand_FilterMethod(t *testing.T) {
	out, err := execWithTestFactory(context.Background(), nil, "route:list", "--method", "post", "--json")
	require.NoError(t, err)

	var routes []console.RouteEntry
	require.NoError(t, json.Unmarshal([]byte(out), &routes))
	require.NotEmpty(t, routes)
	for _, r := range routes {
		assert.Equal(t, "POST", r.Method)
	}
}

func TestRouteListCommand_FilterPath(t *testing.T) {
	out, err := execWithTestFactory(context.Background(), nil, "route:list", "--path", "/v1/auth", "--json")
	require.NoError(t, err)

	var routes []console.RouteEntry
	require.NoError(t, json.Unmarshal([]byte(out), &routes))
	require.NotEmpty(t, routes)
	for _, r := range routes {
		assert.Contains(t, r.Path, "/v1/auth")
	}
}

func TestRouteListCommand_Sorting(t *testing.T) {
	outAsc, err := execWithTestFactory(context.Background(), nil, "route:list", "--sort", "path", "--json")
	require.NoError(t, err)

	var routesAsc []console.RouteEntry
	require.NoError(t, json.Unmarshal([]byte(outAsc), &routesAsc))
	require.NotEmpty(t, routesAsc)

	outDesc, err := execWithTestFactory(context.Background(), nil, "route:list", "--sort", "path", "--reverse", "--json")
	require.NoError(t, err)

	var routesDesc []console.RouteEntry
	require.NoError(t, json.Unmarshal([]byte(outDesc), &routesDesc))
	require.Len(t, routesDesc, len(routesAsc))
	assert.Equal(t, routesAsc[0].Path, routesDesc[len(routesDesc)-1].Path)
}

func TestRouteListCommand_InteractiveFallbackInNonTTY(t *testing.T) {
	out, err := execWithTestFactory(context.Background(), nil, "route:list", "-i")
	require.NoError(t, err)
	assert.Contains(t, out, "Interactive mode requires a TTY terminal")
	assert.Contains(t, out, "HTTP Route List")
}
