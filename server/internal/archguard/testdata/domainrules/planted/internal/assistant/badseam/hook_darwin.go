package badseam

type Hook struct{}

func (h Hook) run()             {}
func (h Hook) Callback() func() { return h.run }
