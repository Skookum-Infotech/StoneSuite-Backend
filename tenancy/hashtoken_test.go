package tenancy

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestHashToken(t *testing.T) {
	// SHA-256("abc")
	assert.Equal(t, "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad", HashToken("abc"))
	assert.NotEqual(t, HashToken("a"), HashToken("b"))
}
