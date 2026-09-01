package main

import (
	"errors"
	"os/exec"
	"strings"
	"testing"
)

type failingWriter struct{ n int }

func (f *failingWriter) Write(p []byte) (int, error) {
	f.n++
	return 0, errors.New("broken pipe")
}

// A build's log stream is a recording device. When it breaks, os/exec
// surfaces the write error through cmd.Wait(), which used to mark a step
// that had exited 0 as failed — a green suite reported red, with the line
// explaining it written to the same dead pipe.
func TestBrokenLogSinkDoesNotFailTheStep(t *testing.T) {
	sink := &logSink{w: &failingWriter{}}

	cmd := exec.Command("sh", "-c", "echo out; echo err >&2; exit 0")
	cmd.Stdout, cmd.Stderr = sink, sink
	if err := cmd.Run(); err != nil {
		t.Fatalf("step reported failed because its log sink broke: %v", err)
	}
	if !sink.broken() {
		t.Error("sink did not record that the stream was lost")
	}
}

// The converse: a step that genuinely fails still fails.
func TestGenuineStepFailureStillFails(t *testing.T) {
	sink := &logSink{w: &failingWriter{}}
	cmd := exec.Command("sh", "-c", "exit 3")
	cmd.Stdout, cmd.Stderr = sink, sink
	if err := cmd.Run(); err == nil {
		t.Fatal("a step exiting 3 was reported as success")
	}
}

// Once the stream is gone the sink stops touching it, rather than calling a
// broken pipe once per write for the rest of a long build.
func TestLogSinkStopsWritingAfterFailure(t *testing.T) {
	w := &failingWriter{}
	sink := &logSink{w: w}
	for i := 0; i < 5; i++ {
		if _, err := sink.Write([]byte("x")); err != nil {
			t.Fatalf("sink returned an error: %v", err)
		}
	}
	if w.n != 1 {
		t.Errorf("underlying writer called %d times, want 1", w.n)
	}
}

// A healthy sink still forwards everything.
func TestHealthyLogSinkForwards(t *testing.T) {
	var b strings.Builder
	sink := &logSink{w: &b}
	cmd := exec.Command("sh", "-c", "echo hello")
	cmd.Stdout, cmd.Stderr = sink, sink
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	if got := b.String(); !strings.Contains(got, "hello") {
		t.Errorf("sink dropped output: %q", got)
	}
	if sink.broken() {
		t.Error("healthy sink reported broken")
	}
}
