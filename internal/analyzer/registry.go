package analyzer

func DefaultSources() []Source {
	return []Source{
		vetSource{},
		staticcheckSource{},
		customSource{},
		semgrepSource{},
	}
}
