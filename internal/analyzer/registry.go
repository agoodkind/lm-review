package analyzer

// DefaultSources returns the analyzer sources used by [Run] when a
// [Config] does not select a custom set.
func DefaultSources() []Source {
	return []Source{
		vetSource{},
		staticcheckSource{},
		customSource{},
		semgrepSource{},
	}
}
