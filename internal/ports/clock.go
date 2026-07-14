package ports

import "time"

type Ticker interface {
	C() <-chan time.Time
	Stop()
}

type Clock interface {
	Now() time.Time
	NewTicker(time.Duration) Ticker
}
