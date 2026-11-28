package badseam

type Hook struct{ run func() }

func (h Hook) Callback() func() { return h.run } // core-accessor
