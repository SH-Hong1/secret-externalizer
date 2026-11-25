package bao

import "time"

// MaxBatchBytes exposes maxBatchBytes to the tests in package bao_test.
const MaxBatchBytes = maxBatchBytes

// RequestLimit returns the limit the official client puts on each request
// of c, which zero means it does not.
func RequestLimit(c *Client) time.Duration { return c.api.ClientTimeout() }
