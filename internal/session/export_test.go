package session

// KeyForTest returns the valkey key that holds the record of the given kind
// for id.
func KeyForTest(kind string, id ID) string { return key(kind, id) }
