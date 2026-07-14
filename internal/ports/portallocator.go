package ports

import "context"

type PortAllocator interface {
	Candidates(context.Context, int, int) ([]int, error)
}
