package proxy

import "bytes"

// markerPrefix starts an aish marker: OSC 6973 ; <kind> ; <payload> BEL.
var markerPrefix = []byte("\x1b]6973;")

const maxMarker = 64 << 10

// Marker is one decoded aish OSC sequence.
type Marker struct {
	Kind    string
	Payload string
}

// Filter removes aish markers from a byte stream. Markers may be split
// across reads, so incomplete tails are held back until the next call.
type Filter struct {
	pending []byte
}

// Feed returns the bytes to pass through and the markers found, in order.
// Each marker is reported together with the passthrough bytes preceding it,
// so callers can attribute output to the right command.
func (f *Filter) Feed(p []byte, onText func([]byte), onMarker func(Marker)) {
	data := p
	if len(f.pending) > 0 {
		data = append(f.pending, p...)
		f.pending = nil
	}
	for len(data) > 0 {
		i := bytes.IndexByte(data, 0x1b)
		if i < 0 {
			onText(data)
			return
		}
		rest := data[i:]
		if len(rest) < len(markerPrefix) {
			if bytes.HasPrefix(markerPrefix, rest) {
				if i > 0 {
					onText(data[:i])
				}
				f.pending = append([]byte{}, rest...)
				return
			}
		} else if bytes.HasPrefix(rest, markerPrefix) {
			end := bytes.IndexByte(rest, 0x07)
			if end < 0 {
				if len(rest) > maxMarker {
					// Not a real marker; give up and pass it through.
					onText(data)
					return
				}
				if i > 0 {
					onText(data[:i])
				}
				f.pending = append([]byte{}, rest...)
				return
			}
			if i > 0 {
				onText(data[:i])
			}
			onMarker(parseMarker(rest[len(markerPrefix):end]))
			data = rest[end+1:]
			continue
		}
		onText(data[:i+1])
		data = data[i+1:]
	}
}

func parseMarker(b []byte) Marker {
	kind, payload, _ := bytes.Cut(b, []byte{';'})
	return Marker{Kind: string(kind), Payload: string(payload)}
}
