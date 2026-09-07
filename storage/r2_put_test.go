package storage

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestPut_NilClient(t *testing.T) {
	var c *Client
	err := c.Put(context.Background(), "k", "application/pdf", []byte("x"))
	assert.ErrorIs(t, err, ErrStorageNotConfigured)
}

func TestGet_NilClient(t *testing.T) {
	var c *Client
	_, err := c.Get(context.Background(), "k")
	assert.ErrorIs(t, err, ErrStorageNotConfigured)
}
