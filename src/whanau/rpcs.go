package whanau

type LookupArgs struct {
	Key        KeyType
	RoutedFrom []string // servers that have already tried to serve this key
}

type LookupReply struct {
	Err   Err
	Value TrueValueType
}

// TODO hashing for debugging?
type PutArgs struct {
	Key   KeyType
	Value TrueValueType
}

type PutReply struct {
	Err Err
}

type RandomWalkArgs struct {
	Steps int
}

type RandomWalkReply struct {
	// TODO return record?
	Server  string
	Err     Err
}

type GetIdArgs struct {
	Layer int
}

type GetIdReply struct {
	Key string
	Err Err
}

type InitPaxosClusterArgs struct {
	RequestServer string
	Phase string
	Action string

	// only populated in phase 2
	KeyMap map[KeyType]TrueValueType
	Servers []string
}

type InitPaxosClusterReply struct {
	Reply string
	Err   Err
}

// Types only used for testing
type PutIdArgs struct {
    Key int
    Value string
}

type PutIdReply struct {
    Err Err
}
