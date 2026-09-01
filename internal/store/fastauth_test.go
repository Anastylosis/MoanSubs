package store

// The full 600k-iteration cost belongs to production logins, not to every
// test that creates an account.
func init() { UseFastPasswordHashing() }
