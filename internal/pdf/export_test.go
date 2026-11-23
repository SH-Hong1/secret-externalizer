package pdf

// SetMaxBytes lowers the largest PDF that c returns, so that tests need not
// send 64 MiB.
func SetMaxBytes(c *Client, n int64) { c.maxBytes = n }
