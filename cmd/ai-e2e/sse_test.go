package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestParseSSEFrames(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want []sseFrame
	}{
		{
			name: "single token frame",
			raw:  "event: token\ndata: \"hi\"\n\n",
			want: []sseFrame{{Event: "token", Data: `"hi"`}},
		},
		{
			name: "sequence with ping comments interleaved",
			raw:  "event: token\ndata: \"a\"\n\n: ping\n\nevent: token\ndata: \"b\"\n\n: ping\n\nevent: done\ndata: {\"answer\":\"ab\"}\n\n",
			want: []sseFrame{
				{Event: "token", Data: `"a"`},
				{Event: "token", Data: `"b"`},
				{Event: "done", Data: `{"answer":"ab"}`},
			},
		},
		{
			name: "empty body",
			raw:  "",
			want: nil,
		},
		{
			name: "only ping comments",
			raw:  ": ping\n\n: ping\n\n",
			want: nil,
		},
		{
			name: "error frame",
			raw:  "event: error\ndata: {\"success\":false,\"message\":\"boom\"}\n\n",
			want: []sseFrame{{Event: "error", Data: `{"success":false,"message":"boom"}`}},
		},
		{
			name: "crlf line endings",
			raw:  "event: token\r\ndata: \"x\"\r\n\r\n",
			want: []sseFrame{{Event: "token", Data: `"x"`}},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := parseSSEFrames(tc.raw)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestDecodeTokenData(t *testing.T) {
	tests := []struct {
		name    string
		data    string
		want    string
		wantErr bool
	}{
		{name: "simple string", data: `"hello"`, want: "hello"},
		{name: "escaped quote", data: `"he said \"hi\""`, want: `he said "hi"`},
		{name: "empty string", data: `""`, want: ""},
		{name: "not json", data: `hello`, wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := decodeTokenData(tc.data)
			if tc.wantErr {
				assert.Error(t, err)
				return
			}
			assert.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestDecodeObjectData(t *testing.T) {
	tests := []struct {
		name    string
		data    string
		want    map[string]any
		wantErr bool
	}{
		{
			name: "done payload",
			data: `{"answer":"hi","conversation_id":"abc"}`,
			want: map[string]any{"answer": "hi", "conversation_id": "abc"},
		},
		{name: "not json", data: `nope`, wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := decodeObjectData(tc.data)
			if tc.wantErr {
				assert.Error(t, err)
				return
			}
			assert.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}
