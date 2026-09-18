package objstore

import "time"

// SetClock moves the Store's idea of now, so a test can sign a URL that has
// already expired without sleeping until it does.
func (s *Store) SetClock(now func() time.Time) { s.now = now }
