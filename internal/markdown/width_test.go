package markdown

import (
	"os"
	"testing"

	"github.com/mattn/go-runewidth"
)

// The expected screens are drawn with ambiguous characters one column
// wide; runewidth would take the width from the locale of whoever runs
// the tests. Setting the field is enough: DefaultCondition reads it on
// every call and has no lookup table to rebuild, since nothing calls
// CreateLUT.
func TestMain(m *testing.M) {
	runewidth.DefaultCondition.EastAsianWidth = false
	os.Exit(m.Run())
}
