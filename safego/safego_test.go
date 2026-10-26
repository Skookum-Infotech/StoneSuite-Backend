package safego

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDo(t *testing.T) {
	tests := []struct {
		name      string
		fn        func()
		wantPanic bool
		wantValue any
	}{
		{name: "no panic", fn: func() {}, wantPanic: false},
		{name: "string panic", fn: func() { panic("boom") }, wantPanic: true, wantValue: "boom"},
		{name: "error panic", fn: func() { panic(errors.New("bad input")) }, wantPanic: true},
		{name: "nil map write", fn: func() {
			var m map[string]int
			m["x"] = 1
		}, wantPanic: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := Do(context.Background(), "test."+tt.name, tt.fn)
			if !tt.wantPanic {
				assert.NoError(t, err)
				return
			}
			var pe *PanicError
			require.ErrorAs(t, err, &pe)
			assert.Equal(t, "test."+tt.name, pe.Name)
			if tt.wantValue != nil {
				assert.Equal(t, tt.wantValue, pe.Value)
			}
			assert.Contains(t, err.Error(), "panic")
		})
	}
}

func TestDo_RunsDefersBeforeRecovering(t *testing.T) {
	released := false
	err := Do(context.Background(), "test.defer", func() {
		defer func() { released = true }()
		panic("boom")
	})
	require.Error(t, err)
	assert.True(t, released, "fn's own defers (lock release, wg.Done) must still run")
}

func TestGo_PanicDoesNotEscapeAndCallerCanStillWait(t *testing.T) {
	var wg sync.WaitGroup
	wg.Add(1)
	Go(context.Background(), "test.go", func() {
		defer wg.Done()
		panic("boom")
	})

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("WaitGroup never released after a panicking Go")
	}
}
