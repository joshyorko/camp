package host

import (
	"time"

	"github.com/joshyorko/camp/internal/ports"
)

type Clock struct{}

func NewClock() *Clock { return &Clock{} }

func (c *Clock) Now() time.Time { return time.Now() }

func (c *Clock) NewTicker(interval time.Duration) ports.Ticker {
	return ticker{Ticker: time.NewTicker(interval)}
}

type ticker struct{ *time.Ticker }

func (t ticker) C() <-chan time.Time { return t.Ticker.C }
