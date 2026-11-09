package interp

import "fmt"

// FrameCell is a heap cell for one captured local variable, shared by the
// owning frame and its closures.
type FrameCell struct {
	v Value
}

func (c *FrameCell) value() Value { return c.v }
func (c *FrameCell) set(v Value)  { c.v = v }

// Function is a compiled closure prototype, optionally with captured cells.
type Function struct {
	code      []uint8
	consts    []Value
	name      string
	nargs     int
	nreq      int
	variadic  bool
	hasOpt    bool
	hasSelf   bool
	frameSize int         // nargs + internal defines (self-name extra when hasSelf)
	capSlots  []int       // own frame slots that need cells
	capBySlot map[int]int // frame slot -> capture index
	capSrc    []uint32    // per upval: lev<<16 | slot in parent's env
	capSymIDs []uint32    // per upval: symbol id (for parent resolution)
	capBySym  map[uint32]int
	upvals    []*FrameCell
	selfRef   bool // this function is recursively callable through its self slot
	selfID    uint32
	env       *Module // module namespace for global refs (nil = ambient globals)
}

// Builtin is a Go-implemented primitive.
type Builtin struct {
	name   string
	f      func(g *Interp, args []Value) Value
	opcode uint8 // 0 = not inlinable
	nargs  int   // -1 = variadic; -2 = >=1; else exact count
}

func (b *Builtin) String() string { return fmt.Sprintf("#<builtin %s>", b.name) }
