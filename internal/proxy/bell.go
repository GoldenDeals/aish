package proxy

// bell rings the terminal's bell as the agent opens a question, if
// ask_bell is on: a question in a tmux window or a terminal tab in the
// background says nothing of itself otherwise, while its time runs out.
// tmux marks the window, the terminal the tab. Written past what the
// viewer and the panes hold, as the question may be: the bell is for now.
// The proxy's own firm questions (confirm) do not ring: the user has just
// run what asks them.
func (p *Proxy) bell() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.askBell && p.size != nil {
		p.write([]byte("\a"))
	}
}
