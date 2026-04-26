package domain

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDeployError_PrimaryOnly(t *testing.T) {
	primary := errors.New("link broken")
	e := &DeployError{Op: "deploying foo.esp", Primary: primary}

	assert.Equal(t, "deploying foo.esp: link broken", e.Error())
	require.ErrorIs(t, e, primary)
}

func TestDeployError_WithRollback(t *testing.T) {
	primary := errors.New("link broken")
	rb := errors.New("undo blocked")
	e := &DeployError{Op: "deploying foo.esp", Primary: primary, Rollback: rb}

	assert.Equal(t, "deploying foo.esp: link broken; rollback failed: undo blocked", e.Error())
	require.ErrorIs(t, e, primary)
	require.ErrorIs(t, e, rb)
}

func TestDeployError_WithCleanup(t *testing.T) {
	primary := errors.New("link broken")
	cl := errors.New("dst remains")
	e := &DeployError{Op: "deploying foo.esp", Primary: primary, Cleanup: cl}

	assert.Equal(t, "deploying foo.esp: link broken; cleanup failed: dst remains", e.Error())
	require.ErrorIs(t, e, primary)
	require.ErrorIs(t, e, cl)
}

func TestDeployError_AllThree(t *testing.T) {
	primary := errors.New("link broken")
	cl := errors.New("dst remains")
	rb := errors.New("undo blocked")
	e := &DeployError{Op: "deploying foo.esp", Primary: primary, Cleanup: cl, Rollback: rb}

	assert.Equal(t, "deploying foo.esp: link broken; cleanup failed: dst remains; rollback failed: undo blocked", e.Error())
	require.ErrorIs(t, e, primary)
	require.ErrorIs(t, e, cl)
	require.ErrorIs(t, e, rb)
}

func TestDeployError_NoOp(t *testing.T) {
	primary := errors.New("ctx cancelled")
	e := &DeployError{Primary: primary}
	assert.Equal(t, "ctx cancelled", e.Error(), "empty Op should not produce a leading colon")
}

func TestDeployError_AsTypedSentinel(t *testing.T) {
	myErrInst := &deployTestErr{msg: "x"}
	// errors.As should find the typed error wrapped inside Cleanup or Rollback.
	e := &DeployError{Op: "deploying x", Primary: errors.New("p"), Cleanup: myErrInst}

	var target *deployTestErr
	require.True(t, errors.As(e, &target), "errors.As should reach into Cleanup")
	assert.Same(t, myErrInst, target)
}

// deployTestErr is a typed error used to assert errors.As reaches into the
// composite's Cleanup / Rollback slots.
type deployTestErr struct{ msg string }

func (e *deployTestErr) Error() string { return e.msg }
