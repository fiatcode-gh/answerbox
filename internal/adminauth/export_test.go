package adminauth

// ThrottleSize reports how many keys the login throttle holds.
func ThrottleSize(s *Service) int {
	s.throttle.mu.Lock()
	defer s.throttle.mu.Unlock()
	return len(s.throttle.entries)
}
