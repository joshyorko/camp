package setupui

import (
	"math/rand/v2"
	"time"

	tea "charm.land/bubbletea/v2"
)

const (
	starCount = 100
	starSpeed = 0.03
	starMinZ  = 0.1
	starMaxZ  = 3.0
	starTick  = 33 * time.Millisecond
)

var leftDots = [4]rune{0x01, 0x02, 0x04, 0x40}
var rightDots = [4]rune{0x08, 0x10, 0x20, 0x80}

type starfieldTickMsg struct{}

type star struct {
	x, y, z float64
}

type starCell struct {
	ch     rune
	bright bool
}

// Starfield is a deterministic adaptation of Basecamp/ONCE's animated
// 2x4 Braille star projection.
type Starfield struct {
	width, height int
	stars         []star
	rng           *rand.Rand
	grid          [][]starCell
}

func NewStarfield(seed uint64) *Starfield {
	return &Starfield{rng: rand.New(rand.NewPCG(seed, seed^0xCA37))}
}

func hash(x, y, seed uint64) uint64 {
	h := x*0x9E3779B97F4A7C15 ^ y*0xC2B2AE3D27D4EB4F ^ seed*0x165667B19E3779F9
	h ^= h >> 33
	h *= 0xFF51AFD7ED558CCD
	return h ^ h>>33
}

func (s *Starfield) Init() tea.Cmd { return s.scheduleTick() }

func (s *Starfield) Update(msg tea.Msg) tea.Cmd {
	if _, ok := msg.(starfieldTickMsg); !ok {
		return nil
	}
	s.step()
	s.ComputeGrid()
	return s.scheduleTick()
}

func (s *Starfield) Resize(width, height int) {
	if width == s.width && height == s.height {
		return
	}
	s.width, s.height = width, height
	s.stars = make([]star, starCount)
	for i := range s.stars {
		s.stars[i] = s.randomStar()
	}
	s.grid = make([][]starCell, max(height, 0))
	for row := range s.grid {
		s.grid[row] = make([]starCell, max(width, 0))
	}
	s.ComputeGrid()
}

func (s *Starfield) ComputeGrid() {
	if s.width <= 0 || s.height <= 0 {
		return
	}
	for row := range s.grid {
		clear(s.grid[row])
	}
	subW, subH := s.width*2, s.height*4
	centerX, centerY := float64(subW)/2, float64(subH)/2
	for i := range s.stars {
		st := &s.stars[i]
		if st.z <= 0 {
			continue
		}
		sxi, syi := int(centerX+st.x/st.z), int(centerY+st.y/st.z)
		if sxi < 0 || sxi >= subW || syi < 0 || syi >= subH {
			continue
		}
		cell := &s.grid[syi/4][sxi/2]
		if cell.ch == 0 {
			cell.ch = 0x2800
		}
		dotRow := 3 - syi%4
		if sxi%2 == 0 {
			cell.ch |= leftDots[dotRow]
		} else {
			cell.ch |= rightDots[dotRow]
		}
		if st.z < starMaxZ/2 {
			cell.bright = true
		}
	}
}

func (s *Starfield) Paint(c *Canvas, skyRows int, pal Palette) {
	rows := min(skyRows, min(s.height, c.Height()))
	for y := 0; y < rows; y++ {
		for x := 0; x < min(s.width, c.Width()); x++ {
			cell := s.grid[y][x]
			if cell.ch == 0 {
				continue
			}
			col := pal.StarDim
			if cell.bright {
				col = pal.StarBright
			}
			c.Set(x, y, cell.ch, col)
		}
	}
}

func (s *Starfield) step() {
	subW, subH := s.width*2, s.height*4
	centerX, centerY := float64(subW)/2, float64(subH)/2
	for i := range s.stars {
		st := &s.stars[i]
		st.z -= starSpeed
		if st.z <= starMinZ {
			s.stars[i] = s.randomStar()
			continue
		}
		sx, sy := centerX+st.x/st.z, centerY+st.y/st.z
		if sx < 0 || sx >= float64(subW) || sy < 0 || sy >= float64(subH) {
			s.stars[i] = s.randomStar()
		}
	}
}

func (s *Starfield) randomStar() star {
	spread := float64(max(s.width, s.height))
	return star{
		x: (s.rng.Float64() - 0.5) * spread,
		y: (s.rng.Float64() - 0.5) * spread,
		z: starMinZ + s.rng.Float64()*(starMaxZ-starMinZ),
	}
}

func (s *Starfield) scheduleTick() tea.Cmd {
	return tea.Tick(starTick, func(time.Time) tea.Msg { return starfieldTickMsg{} })
}
