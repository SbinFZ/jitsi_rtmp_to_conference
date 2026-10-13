package xmpp

// Done closes when the transport shuts down. Stanzas is intentionally not closed
// by upstream, so consumers must select on Done rather than range indefinitely.
func (c *Conn) Done() <-chan struct{} { return c.closed }
