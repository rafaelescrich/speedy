package whanau

const (
	OK          = "OK"
	ErrNoKey    = "ErrNoKey"
	ErrRandWalk = "ErrRandWalk"
)

// for 2PC
const (
	// Action
	Commit = "commit"
	Abort  = "abort"

	// Reply
	Accept = "accept"
	Reject = "reject"

	// Phase
	PhaseOne = "p1"
	PhaseTwo = "p2"
)

type Err string

type KeyType string

type ValueType struct {
	Servers []string
}

type TrueValueType string

// Key value pair
type Record struct {
	Key   KeyType
	Value ValueType
}

// tuple for (id, address) pairs used in finger table
type Finger struct {
	Id      KeyType
	Address string
}

// Global Parameters
const (
	W         = 2 // mixing time of honest region
	RD        = 2 // size of db
	RF        = 2 // size of fingertable (per layer)
	RS        = 1 // size of succ records
	L         = 3 // number of layers
	PaxosSize = 5
	PaxosWalk = 5
)
