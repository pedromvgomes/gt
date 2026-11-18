package tests

import (
	"io"
	"strings"

	"go.vipengele.org/cli/spec"
)

// testInvocation builds the invocation an internal package writes through and
// reads its answers from. stdin is what a person would type; interactive is
// what Env.IsTerminal reports for every stream, so it decides whether a prompt
// asks or answers with its default. A nil writer discards. Colour is off, so
// output is plain text with the message glyphs.
func testInvocation(stdin string, stdout, stderr io.Writer, interactive bool) *spec.Invocation {
	inv := spec.NewInvocation(&spec.Env{
		Stdin:    strings.NewReader(stdin),
		Stdout:   stdout,
		Stderr:   stderr,
		Terminal: func(spec.Stream) bool { return interactive },
	}, nil, nil)
	inv.Output = spec.NewOutput(spec.OutputOptions{NoColorStdout: true, NoColorStderr: true})
	return inv
}

// quietInvocation is a non-interactive invocation with --quiet set and every
// stream discarded.
func quietInvocation() *spec.Invocation {
	inv := testInvocation("", nil, nil, false)
	inv.Output = spec.NewOutput(spec.OutputOptions{Quiet: true, NoColorStdout: true, NoColorStderr: true})
	return inv
}
