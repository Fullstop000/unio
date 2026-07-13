package driver

import (
	"context"
	"errors"
	"testing"
)

func TestSessionDataTokenStatisticsUsesRawData(t *testing.T) {
	wantRaw := RawSessionData{Format: SessionDataJSONL, Data: []byte("raw")}
	wantUsage := TokenUsage{InputTokens: 3, OutputTokens: 2}
	data := NewSessionData(
		context.Background(),
		func(context.Context) (RawSessionData, error) { return wantRaw, nil },
		func(_ context.Context, raw RawSessionData) (TokenUsage, error) {
			if raw.Format != wantRaw.Format || string(raw.Data) != string(wantRaw.Data) {
				t.Fatalf("parser raw data = %#v; want %#v", raw, wantRaw)
			}
			return wantUsage, nil
		},
	)

	got, err := data.TokenStatistics()
	if err != nil {
		t.Fatal(err)
	}
	if got != wantUsage {
		t.Fatalf("usage = %#v; want %#v", got, wantUsage)
	}
}

func TestSessionDataHonorsFactoryContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	loaded := false
	data := NewSessionData(ctx, func(context.Context) (RawSessionData, error) {
		loaded = true
		return RawSessionData{}, nil
	}, nil)

	_, err := data.Raw()
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Raw error = %v; want context.Canceled", err)
	}
	if loaded {
		t.Fatal("loader ran after context cancellation")
	}
}

func TestSessionDataUnsupportedOperations(t *testing.T) {
	data := NewSessionData(context.Background(), nil, nil)
	if _, err := data.Raw(); !errors.Is(err, NewUnsupportedError("")) {
		t.Fatalf("Raw error = %v; want unsupported", err)
	}
	if _, err := data.TokenStatistics(); !errors.Is(err, NewUnsupportedError("")) {
		t.Fatalf("TokenStatistics error = %v; want unsupported", err)
	}
}
