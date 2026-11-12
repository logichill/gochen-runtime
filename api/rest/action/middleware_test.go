package action

import auth "gochen/auth"

import (
	"context"
	"testing"

	"gochen/errors"
	"gochen/testkit/require"
)

type allowChecker struct{}

func (allowChecker) RequireAction(context.Context, string) error { return nil }

var _ auth.IActionChecker = allowChecker{}

func TestFastDenyRejectsInvalidConfigurationAtConstruction(t *testing.T) {
	_, err := FastDeny(nil, "report:api:read")
	require.Error(t, err)
	require.True(t, errors.Is(err, errors.InvalidInput))

	_, err = FastDeny(allowChecker{}, "invalid")
	require.Error(t, err)
	require.True(t, errors.Is(err, errors.InvalidInput))
}

func TestFastDenyAcceptsValidConfiguration(t *testing.T) {
	middleware, err := FastDeny(allowChecker{}, "report:api:read")
	require.NoError(t, err)
	require.NotNil(t, middleware)
}
