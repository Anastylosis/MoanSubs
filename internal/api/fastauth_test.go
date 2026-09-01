package api

import "github.com/Anastylosis/MoanSubs/internal/store"

// The full 600k-iteration cost belongs to production logins, not to every
// test that creates an account or logs a session in.
func init() { store.UseFastPasswordHashing() }
