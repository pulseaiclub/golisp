package interp

// Bytecode opcodes.
const (
	opNop uint8 = iota
	opDup
	opPop
	opCall  // u8 nargs
	opTCall // u8 nargs
	opJmp   // i32 rel
	opBrf   // i32 rel (pop)
	opBrt   // i32 rel (pop)
	opRet
	opLoadV // u16 const idx
	opLoadT
	opLoadF
	opLoadNil
	opLoad0
	opLoad1
	opLoadI8  // i8
	opLoadG   // u32 symid
	opLoadA   // u16 frame slot
	opLoadC   // u8 level, u8 slot
	opSetG    // u32 symid
	opSetA    // u16
	opSetC    // u8 level, u8 slot
	opClosure // u8 ncap, then ncap x u32 ref (bit31: 1=frame slot, 0=parent upval)
	opArgc    // u8
	opVArgc   // u8
	opOptArgs // u8 nreq, u8 ntot
	opBrBound // u16 slot, i32 rel
	opTryCatch
	opFor

	opAddN // u8 nargs
	opSubN // u8
	opMulN // u8
	opDivN // u8
	opAdd2
	opSub2
	opNeg
	opIDiv
	opNumEq
	opNumLt
	opCompare

	opEq
	opEqv
	opEqual
	opAtomp
	opNot
	opNullp
	opBooleanp
	opSymbolp
	opNumberp
	opBoundp
	opPairp
	opBuiltinp
	opVectorp
	opFixnump
	opFunctionp

	opCons
	opListN
	opCar
	opCdr
	opSetCar
	opSetCdr
	opCadr

	opApply
	opTApply
	opVectorN
	opAref
	opAset

	opBrNe
	opBrNN
	opBrN
	opLoadA0
	opLoadA1
	opLoadC00
	opLoadC01

	// Go member access: sym prefix, member-name const, dotted fallback sym,
	// and a mode byte (0 = value, 1 = callable).
	opGoMember // u32 prefixSym, u16 memberConst, u32 fullSym, u8 mode
	opGoSet    // u32 prefixSym, u16 memberConst

	// Direct Lisp calls. Self calls use the current function; known calls
	// refer to a boxed Function in the constant table.
	opSelfCall   // u8 nargs
	opSelfTCall  // u8 nargs
	opKnownCall  // u16 const index, u8 nargs
	opKnownTCall // u16 const index, u8 nargs
	opBrNumLt    // i32 rel; consumes two numeric operands
	opBrNumEq    // i32 rel; consumes two numeric operands

	numOps
)

var opNames = []string{
	opNop: "nop", opDup: "dup", opPop: "pop", opCall: "call", opTCall: "tcall",
	opJmp: "jmp", opBrf: "brf", opBrt: "brt", opRet: "ret",
	opLoadV: "loadv", opLoadT: "loadt", opLoadF: "loadf", opLoadNil: "loadnil",
	opLoad0: "load0", opLoad1: "load1", opLoadI8: "loadi8",
	opLoadG: "loadg", opLoadA: "loada", opLoadC: "loadc",
	opSetG: "setg", opSetA: "seta", opSetC: "setc",
	opClosure: "closure", opArgc: "argc", opVArgc: "vargc",
	opOptArgs: "optargs", opBrBound: "brbound", opTryCatch: "trycatch", opFor: "for",
	opAddN: "+", opSubN: "-", opMulN: "*", opDivN: "/",
	opAdd2: "add2", opSub2: "sub2", opNeg: "neg", opIDiv: "div0",
	opNumEq: "=", opNumLt: "<", opCompare: "compare",
	opEq: "eq?", opEqv: "eqv?", opEqual: "equal?", opAtomp: "atom?",
	opNot: "not", opNullp: "null?", opBooleanp: "boolean?", opSymbolp: "symbol?",
	opNumberp: "number?", opBoundp: "bound?", opPairp: "pair?",
	opBuiltinp: "builtin?", opVectorp: "vector?", opFixnump: "fixnum?",
	opFunctionp: "function?",
	opCons:      "cons", opListN: "list", opCar: "car", opCdr: "cdr",
	opSetCar: "set-car!", opSetCdr: "set-cdr!", opCadr: "cadr",
	opApply: "apply", opTApply: "tapply", opVectorN: "vector",
	opAref: "aref", opAset: "aset!",
	opBrNe: "brne", opBrNN: "brnn", opBrN: "brn",
	opLoadA0: "loada0", opLoadA1: "loada1", opLoadC00: "loadc00", opLoadC01: "loadc01",
	opGoMember: "gomember", opGoSet: "goset",
	opSelfCall: "self-call", opSelfTCall: "self-tcall",
	opKnownCall: "known-call", opKnownTCall: "known-tcall",
	opBrNumLt: "br-num-lt", opBrNumEq: "br-num-eq",
}
