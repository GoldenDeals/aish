// Package caret is the bus contract between the module that shows the conversation and the one that marks
// where the model writes in it.
//
// The conversation, a tui.Fullscreen element, announces a Source once from Start. The marking module opens
// its own Fullscreen element above it: every Fullscreen element gets the same area, so the Source's positions
// are in its coordinates too, and the cells it doesn't set leave the conversation in view. The compositor
// draws lower z first, so Last called from the marker's Draw is this frame's.
package caret

import uv "github.com/charmbracelet/ultraviolet"

// Announce is event.Event[Source].
const Announce = "tui.caret.announce"

type Source interface {
	// Last is the cell of the conversation's last character at the last frame, a wide one's right half;
	// X is -1 when the last block is still empty: its text starts on row Y. False when the view isn't at the end.
	Last() (uv.Position, bool)
}
