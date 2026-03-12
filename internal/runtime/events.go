package runtime

type EventEmitter interface {
	Emit(name string, payload any)
}

type nopEmitter struct{}

func (nopEmitter) Emit(string, any) {}

func NewNopEmitter() EventEmitter {
	return nopEmitter{}
}
