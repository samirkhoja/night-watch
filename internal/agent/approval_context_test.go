package agent

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"testing"
	"time"
)

func TestApprovalRequestCancelableDuringInputWait(t *testing.T) {
	readerConn, writerConn := net.Pipe()
	defer readerConn.Close()
	defer writerConn.Close()

	approval := NewApprovalManager(readerConn, bufio.NewReader(readerConn), io.Discard)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)

	go func() {
		_, err := approval.Request(ctx, "pwd", ".", 10)
		done <- err
	}()

	time.Sleep(120 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context.Canceled, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("approval request did not unblock after context cancellation")
	}
}
