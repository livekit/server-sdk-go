package lksdk

type Private[T any] struct {
	v T
}

func MakePrivate[T any](f T) Private[T] {
	return Private[T]{f}
}
