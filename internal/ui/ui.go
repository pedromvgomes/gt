package ui

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"
)

const (
	ExitGeneral = 1
	ExitUser    = 2
	ExitSignal  = 130
)

type ExitError struct {
	Code int
	Err  error
}

func (e *ExitError) Error() string {
	return e.Err.Error()
}

func (e *ExitError) Unwrap() error {
	return e.Err
}

func Errorf(code int, format string, args ...any) error {
	return &ExitError{Code: code, Err: fmt.Errorf(format, args...)}
}

func ExitCode(err error) int {
	if err == nil {
		return 0
	}
	var exitErr *ExitError
	if errors.As(err, &exitErr) {
		return exitErr.Code
	}
	return ExitGeneral
}

type UI struct {
	In          io.Reader
	Out         io.Writer
	Err         io.Writer
	Color       bool
	Quiet       bool
	Interactive bool
}

func New(in io.Reader, out, err io.Writer, noColor, quiet bool) *UI {
	return &UI{
		In:          in,
		Out:         out,
		Err:         err,
		Color:       shouldColor(out, noColor),
		Quiet:       quiet,
		Interactive: isTerminal(in),
	}
}

func (u *UI) Info(format string, args ...any) {
	if u.Quiet {
		return
	}
	u.line(u.Out, "INFO:", "\033[0;34m", format, args...)
}

func (u *UI) Success(format string, args ...any) {
	if u.Quiet {
		return
	}
	u.line(u.Out, "SUCCESS:", "\033[0;32m", format, args...)
}

func (u *UI) Warn(format string, args ...any) {
	u.line(u.Err, "WARN:", "\033[0;33m", format, args...)
}

func (u *UI) Error(format string, args ...any) {
	u.line(u.Err, "ERROR:", "\033[1;31m", format, args...)
}

func (u *UI) Prompt(question, def string, required bool, advice string) (string, error) {
	if !u.Interactive {
		if def != "" && !required {
			return def, nil
		}
		if advice != "" {
			return "", Errorf(ExitUser, "%s requires interactive input; %s", question, advice)
		}
		return "", Errorf(ExitUser, "%s requires interactive input", question)
	}

	if def == "" {
		_, _ = fmt.Fprintf(u.Out, "%s: ", question)
	} else {
		_, _ = fmt.Fprintf(u.Out, "%s [%s]: ", question, def)
	}

	line, err := bufio.NewReader(u.In).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", fmt.Errorf("read prompt response: %w", err)
	}
	answer := strings.TrimSpace(line)
	if answer == "" {
		if def != "" {
			return def, nil
		}
		if required {
			return "", Errorf(ExitUser, "%s is required", question)
		}
	}
	return answer, nil
}

func (u *UI) line(w io.Writer, label, color, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	if u.Color {
		_, _ = fmt.Fprintf(w, "%s%s\033[0m %s\n", color, label, msg)
		return
	}
	_, _ = fmt.Fprintf(w, "%s %s\n", label, msg)
}

func shouldColor(w io.Writer, noColor bool) bool {
	if noColor || os.Getenv("NO_COLOR") != "" {
		return false
	}
	file, ok := w.(*os.File)
	return ok && term.IsTerminal(int(file.Fd()))
}

func isTerminal(r io.Reader) bool {
	file, ok := r.(*os.File)
	return ok && term.IsTerminal(int(file.Fd()))
}
