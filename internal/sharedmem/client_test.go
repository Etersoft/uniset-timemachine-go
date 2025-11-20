package sharedmem

import (
	"bytes"
	"context"
	"testing"
)

func TestStdoutClientRequiresWriter(t *testing.T) {
	client := &StdoutClient{}
	err := client.Send(context.Background(), StepPayload{StepID: 1})
	if err == nil {
		t.Fatalf("expected error when writer is nil")
	}
}

func TestStdoutClientWritesPayload(t *testing.T) {
	var buf bytes.Buffer
	client := &StdoutClient{Writer: &buf}
	payload := StepPayload{
		StepID:     2,
		StepTs:     "2024-06-01T00:00:00Z",
		BatchID:    1,
		BatchTotal: 1,
		Updates:    []SensorUpdate{{ID: 42, Value: 3.14}},
	}
	if err := client.Send(context.Background(), payload); err != nil {
		t.Fatalf("Send returned error: %v", err)
	}
	if buf.Len() == 0 {
		t.Fatalf("expected output to be written")
	}
}

func TestBuildSetQueryEmptyUpdates(t *testing.T) {
	if _, err := buildSetQuery("", nil, nil); err == nil {
		t.Fatalf("expected error for empty updates")
	}
}
