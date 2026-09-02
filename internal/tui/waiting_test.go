package tui

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

// Not a terminal — a pipe, a CI log, output redirected to a file — must get the
// plain line. A spinner rendered into a file is thousands of escape sequences
// nobody will ever read.
//
// Every test here runs down this path, because `go test` does not give the
// process a terminal. That is a real limit and worth naming: the ANIMATED path
// is exercised by using the CLI, not by this file.
func TestWaitingFallsBackToAPlainLineWithoutATerminal(t *testing.T) {
	out := captureStderr(t, func() {
		if err := Waiting("resuming box-1", func() error { return nil }); err != nil {
			t.Errorf("Waiting returned %v", err)
		}
	})
	if !strings.Contains(out, "resuming box-1") {
		t.Errorf("stderr = %q, want the label", out)
	}
	if strings.Contains(out, "\x1b[") {
		t.Errorf("wrote escape sequences into a non-terminal: %q", out)
	}
}

// The work's error is the answer. A spinner is a decoration and must never
// stand between a caller and what actually happened.
func TestWaitingReturnsTheWorksError(t *testing.T) {
	want := errors.New("the host said no")
	var got error
	_ = captureStderr(t, func() { got = Waiting("resuming box-1", func() error { return want }) })
	if !errors.Is(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

// And it must actually wait for the work rather than returning as soon as it
// has something to draw.
func TestWaitingDoesNotReturnBeforeTheWorkDoes(t *testing.T) {
	var ran bool
	_ = captureStderr(t, func() {
		_ = Waiting("resuming box-1", func() error {
			time.Sleep(120 * time.Millisecond)
			ran = true
			return nil
		})
	})
	if !ran {
		t.Fatal("Waiting returned before the work finished")
	}
}

func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stderr
	os.Stderr = w
	done := make(chan string, 1)
	go func() {
		var b strings.Builder
		buf := make([]byte, 4096)
		for {
			n, rerr := r.Read(buf)
			b.Write(buf[:n])
			if rerr != nil {
				break
			}
		}
		done <- b.String()
	}()
	fn()
	os.Stderr = old
	_ = w.Close()
	out := <-done
	_ = r.Close()
	return out
}
