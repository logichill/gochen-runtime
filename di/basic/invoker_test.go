package basic

import (
	"strings"
	"testing"

	"gochen/testkit/require"

	"gochen-runtime/di"
	"gochen/errors"
)

func TestContainer_Invoke_RecoversFromPanic(t *testing.T) {
	c := New()

	err := c.Invoke(di.NewInvocation(func() {
		panic("boom")
	}))
	require.Error(t, err)
	require.True(t, errors.Is(err, errors.Internal))
	require.Contains(t, err.Error(), "function panicked")

	var appErr *errors.AppError
	require.True(t, errors.As(err, &appErr))
	require.Equal(t, "boom", appErr.Details()["panic"])
	require.True(t, strings.Contains(appErr.Details()["stack"].(string), "TestContainer_Invoke_RecoversFromPanic"))
}

func TestContainer_InvokePreservesAppErrorCode(t *testing.T) {
	c := New()

	err := c.Invoke(di.NewInvocation(func() error {
		return errors.NewCode(errors.InvalidInput, "bad input")
	}))

	require.Error(t, err)
	require.True(t, errors.Is(err, errors.InvalidInput))
	require.False(t, errors.Is(err, errors.Internal))
}
