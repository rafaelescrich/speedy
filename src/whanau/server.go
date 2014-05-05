// Whanau server serves keys when it can.

package whanau

import (
	"fmt"
	"log"
	"math/rand"
	"net"
	"net/rpc"
	"os"
	"sort"
	"sync"
	"time"
)

//import "builtin"

//import "encoding/gob"

const Debug = 0

func DPrintf(format string, a ...interface{}) (n int, err error) {
	if Debug > 0 {
		log.Printf(format, a...)
	}
	return
}

type WhanauServer struct {
	//// Server variables ////
	mu     sync.Mutex
	l      net.Listener
	me     int
	myaddr string
	dead   bool // for testing

	//// Paxos variables ////
	// map of key -> local WhanauPaxos instance handling the key
	paxosInstances map[KeyType]WhanauPaxos

	//// Routing variables ////
	neighbors []string                  // list of servers this server can talk to
	kvstore   map[KeyType]ValueType     // k/v table used for routing
	pkvstore  map[KeyType]TrueValueType
	ids       []KeyType                 // contains id of each layer
	fingers   [][]Finger                // (id, server name) pairs
	succ      [][]Record                // contains successor records for each layer
	db        []Record                  // sample of records used for constructing struct, according to the paper, the union of all dbs in all nodes cover all the keys =)

	master    []string                  // list of servers for the master cluster; these servers are also trusted
	is_master bool                      // whether the server itself is a master server
	state     State                     // what phase the server is in
	pending   map[KeyType]TrueValueType // this is a list of pending writes
	reqID     int64
}

// for testing
func (ws *WhanauServer) AddToKvstore(key KeyType, value ValueType) {
	ws.kvstore[key] = value
}

func (ws *WhanauServer) GetDB() []Record {
	return ws.db
}

func (ws *WhanauServer) GetSucc() [][]Record {
	return ws.succ
}
func IsInList(val string, array []string) bool {
	for _, v := range array {
		if v == val {
			return true
		}
	}

	return false
}

func (ws *WhanauServer) PaxosGetRPC(args *WhanauServerPaxosGetArgs, reply *WhanauServerPaxosGetReply) error {
	// makes an RPC call to the local paxos instance
	key := args.Key

	ws.mu.Lock()
	ws.reqID += 1
	ws.mu.Unlock()

	rpc_args := &PaxosGetArgs{key, ws.reqID}
	rpc_reply := &PaxosGetReply{}

	paxos_inst := ws.paxosInstances[key]

	ok := call(paxos_inst.myaddr, "WhanuPaxos.PaxosGetRPC", rpc_args, rpc_reply)
	if ok {
		if rpc_reply.Err == OK {
			reply.Value = rpc_reply.Value
		} 
		reply.Err = rpc_reply.Err
	}

	return nil
}

func (ws *WhanauServer) PaxosGet(key KeyType, servers ValueType) TrueValueType {

	args := &WhanauServerPaxosGetArgs{key}
	reply := &WhanauServerPaxosGetReply{}

	for _, server := range servers.Servers {
		ok := call(server, "WhanauServer.PaxosGetRPC", args, reply)
		if ok && reply.Err == OK {
			return reply.Value
		}
	}

	return ""
}

// WHANAU LOOKUP HELPER METHODS

// Returns randomly chosen finger and randomly chosen layer as part of lookup
func (ws *WhanauServer) ChooseFinger(x0 KeyType, key KeyType, nlayers int) (Finger, int) {
	// find all fingers from all layers such that the key falls between x0 and the finger id
	candidateFingers := make([][]Finger, 0)
	// maps index to nonempty layer number
	layerMap := make([]int, 0)
	counter := 0
	for i := 0; i < nlayers; i++ {
		DPrintf("ws.fingers[%d]: %s", i, ws.fingers[i])
		for j := 0; j < len(ws.fingers[i]); j++ {

			// compare x0 <= id <= key on a circle
			id := ws.fingers[i][j].Id
			if x0 <= key {
				if x0 <= id && id <= key {
					if len(candidateFingers) <= counter {
						// only create non empty candidate fingers
						newLayer := make([]Finger, 0)
						candidateFingers = append(candidateFingers, newLayer)
						candidateFingers[counter] = append(candidateFingers[counter], ws.fingers[i][j])
						layerMap = append(layerMap, i)
						counter++
					} else {
						candidateFingers[counter] = append(candidateFingers[counter], ws.fingers[i][j])
					}
				}
			} else {
				// case where x0 > key, compare !(key < x < x0) --> x > x0 or x < key
				if id >= x0 || id <= key {
					if len(candidateFingers) <= counter {
						// only create non empty candidate fingers
						newLayer := make([]Finger, 0)
						candidateFingers = append(candidateFingers, newLayer)
						candidateFingers[counter] = append(candidateFingers[counter], ws.fingers[i][j])
						layerMap = append(layerMap, i)
						counter++
					} else {
						candidateFingers[counter] = append(candidateFingers[counter], ws.fingers[i][j])
					}
				}
			}
		}
	}

	DPrintf("len(candidateFingers): %d, len(layerMap): %d", len(candidateFingers), len(layerMap))
	// pick random layer out of nonempty candidate fingers
	if len(candidateFingers) > 0 {
		randIndex := rand.Intn(len(candidateFingers))
		finger := candidateFingers[randIndex][rand.Intn(len(candidateFingers[randIndex]))]
		return finger, layerMap[randIndex]
	}

	// if can't find any, randomly choose layer and randomly return finger
	// TODO probably shouldn't get here?
	randLayer := rand.Intn(len(ws.fingers))
	randfinger := ws.fingers[randLayer][rand.Intn(len(ws.fingers[randLayer]))]
	return randfinger, randLayer
}

// Query for a key in the successor table
func (ws *WhanauServer) Query(args *QueryArgs, reply *QueryReply) error {
	DPrintf("In Query of server %s", ws.myaddr)
	layer := args.Layer
	key := args.Key
	valueIndex := sort.Search(len(ws.succ[layer]), func(valueIndex int) bool {
		return ws.succ[layer][valueIndex].Key >= key
	})

	if valueIndex < len(ws.succ[layer]) && ws.succ[layer][valueIndex].Key == key {
		DPrintf("In Query: found the key!!!!")
		reply.Value = ws.succ[layer][valueIndex].Value
		DPrintf("reply.Value: %s", reply.Value)
		reply.Err = OK
	} else {
		DPrintf("In Query: did not find key, reply.Value: %s reply.Value.Servers == nil %s", reply.Value, reply.Value.Servers == nil)
		reply.Err = ErrNoKey
	}
	return nil
}

// Try finds the value associated with the key
func (ws *WhanauServer) Try(args *TryArgs, reply *TryReply) error {
	key := args.Key
	nlayers := args.NLayers
	DPrintf("In Try RPC, trying key: %s", key)
	fingerLength := len(ws.fingers[0])
	j := sort.Search(fingerLength, func(i int) bool {
		return ws.fingers[0][i].Id >= key
	})
	j = j % fingerLength
	if j < 0 {
		j = j + fingerLength
	}
	j = (j + fingerLength - 1) % fingerLength
	count := 0
	queryArgs := &QueryArgs{}
	queryReply := &QueryReply{}
	for queryReply.Err != OK && count < TIMEOUT {
		f, i := ws.ChooseFinger(ws.fingers[0][j].Id, key, nlayers)
		queryArgs.Key = key
		queryArgs.Layer = i
		call(f.Address, "WhanauServer.Query", queryArgs, queryReply)
		j = j - 1
		j = j % fingerLength
		if j < 0 {
			j = j + fingerLength
		}

		count++
	}

	if queryReply.Err == OK {
		DPrintf("Found key in Try!")
		value := queryReply.Value
		reply.Value = value
		reply.Err = OK
	} else {
		reply.Err = ErrNoKey
	}
	return nil
}

// TODO this eventually needs to become a real lookup
// Returns paxos cluster for given key
func (ws *WhanauServer) Lookup(args *LookupArgs, reply *LookupReply) error {
	key := args.Key
	nlayers := args.NLayers
	steps := args.Steps

	DPrintf("In Lookup key: %s server %s", key, ws.myaddr)
	addr := ws.myaddr
	count := 0
	tryArgs := &TryArgs{key, nlayers}
	tryReply := &TryReply{}

	for tryReply.Err != OK && count < TIMEOUT {
		call(addr, "WhanauServer.Try", tryArgs, tryReply)
		randomWalkArgs := &RandomWalkArgs{steps}
		randomWalkReply := &RandomWalkReply{}
		call(ws.myaddr, "WhanauServer.RandomWalk", randomWalkArgs, randomWalkReply)
		if randomWalkReply.Err == OK {
			addr = randomWalkReply.Server
		}
		count++
	}

	if tryReply.Err == OK {
		//fmt.Printf("found key after %d tries\n", count)
		value := tryReply.Value
		reply.Value = value
		reply.Err = OK
	} else {
		reply.Err = ErrNoKey
	}

	return nil
}

func (ws *WhanauServer) ClientLookup(args *ClientLookupArgs, reply *ClientLookupReply) error {
	// run the local RPC call to get the
	lookup_args := &LookupArgs{}
	lookup_reply := &LookupReply{}

	lookup_args.NLayers = L
	lookup_args.Steps = W

	ok := call(ws.myaddr, "WhanauServer.Lookup", lookup_args, &lookup_reply)

	if ok {
		server_list := lookup_reply.Value
		var ret_value TrueValueType = ws.PaxosGet(args.Key, server_list)
		reply.Err = OK
		reply.Value = ret_value
	}

	return nil
}

// Client-style lookup on neighboring servers.
// routedFrom is supposed to prevent infinite lookup loops.
// TODO Change to TrueValueType later
/*
func (ws *WhanauServer) NeighborLookup(key KeyType, routedFrom []string) ValueType {
	args := &LookupArgs{}
	args.Key = key
	args.RoutedFrom = routedFrom
	var reply LookupReply
	for _, srv := range ws.neighbors {
		if IsInList(srv, routedFrom) {
			continue
		}

		ok := call(srv, "WhanauServer.Lookup", args, &reply)
		if ok && (reply.Err == OK) {
			return reply.Value
		}
	}

	return ErrNoKey
}
*/

func (ws *WhanauServer) PaxosPut(args *WhanauServerPaxosPutArgs, reply *WhanauServerPaxosPutReply) error {
	
	key := args.Key
	value := args.Value

	paxos_inst := ws.paxosInstances[key].myaddr
	var req_id int64

	ws.mu.Lock()
	ws.reqID += 1
	req_id = ws.reqID
	ws.mu.Unlock()

	rpc_args := &PaxosPutArgs{key, value, req_id}
	rpc_reply := &PaxosPutReply{}
	
	ok := call(paxos_inst, "WhanauPaxos.PaxoPutRPC", rpc_args, rpc_reply)
	if ok {
		reply.Err = rpc_reply.Err
	}
	
	return nil
}

// TODO this eventually needs to become a real put
func (ws *WhanauServer) Put(args *PutArgs, reply *PutReply) error {
	// TODO: needs to 1. find the paxos cluster 2. do a paxos cluster put
	// makes an RPC call to itself, this is kind of weird...

	key := args.Key
	value := args.Value

	rpc_args := &LookupArgs{}
	var rpc_reply LookupReply

	ok := call(ws.myaddr, "WhanauServer.Lookup", rpc_args, rpc_reply)

	if ok {
		if rpc_reply.Err == ErrNoKey {
			// TODO: adds the key to its local pending put list
			// TODO: what happens if a client makes a call to insert the same key
			// to 2 different servers? or 2 different clients making 2 different
			// calls to the same key?
			ws.pending[key] = value
			reply.Err = ErrPending
		} else {
			// TODO: make a paxos request directly to one of the servers
			// TODO error?
			servers := rpc_reply.Value
			for {
				randInt := rand.Intn(len(servers.Servers))
				a := &WhanauServerPaxosPutArgs{}
				r := &WhanauServerPaxosPutReply{}
				ok := call(servers.Servers[randInt], "WhanauServer.PaxosPut", a, r)
				if ok {
					reply.Err = r.Err
				}
			}
		}
	}

	return nil
}

// Random walk
func (ws *WhanauServer) RandomWalk(args *RandomWalkArgs, reply *RandomWalkReply) error {
	steps := args.Steps
	// pick a random neighbor
	randIndex := rand.Intn(len(ws.neighbors))
	neighbor := ws.neighbors[randIndex]
	if steps == 1 {
		reply.Server = neighbor
		reply.Err = OK
	} else {
		args := &RandomWalkArgs{}
		args.Steps = steps - 1
		var rpc_reply RandomWalkReply
		ok := call(neighbor, "WhanauServer.RandomWalk", args, &rpc_reply)
		if ok && (rpc_reply.Err == OK) {
			reply.Server = rpc_reply.Server
			reply.Err = OK
		}
	}

	return nil
}

// Gets the ID from node's local id table
func (ws *WhanauServer) GetId(args *GetIdArgs, reply *GetIdReply) error {
	layer := args.Layer
	//DPrintf("In getid, len(ws.ids): %d layer: %d", len(ws.ids), layer)
	// gets the id associated with a layer
	if 0 <= layer && layer < len(ws.ids) {
		id := ws.ids[layer]
		reply.Key = id
		reply.Err = OK
	}
	return nil
}

// Whanau Routing Protocal methods

// TODO
// Populates routing table
// nlayers = number of layers
// rf = size of finger table
// w = number of steps in random walk
// rd = size of database
// rs = number of nodes to collect samples from
// t = number of successors returned from sample per node
func (ws *WhanauServer) Setup(nlayers int, rf int, w int, rd int, rs int, t int) {
	DPrintf("In Setup of server %s", ws.myaddr)

	// fill up db by randomly sampling records from random walks
	// "The db table has the good property that each honest node’s stored records are frequently represented in other honest nodes’db tables"
	ws.db = ws.SampleRecords(rd, w)

	// reset ids, fingers, succ
	ws.ids = make([]KeyType, 0)
	ws.fingers = make([][]Finger, 0)
	ws.succ = make([][]Record, 0)
	for i := 0; i < nlayers; i++ {
		// populate tables in layers
		ws.ids = append(ws.ids, ws.ChooseID(i))
		//fmt.Printf("Finished ChooseID of server %s, layer %d\n", ws.myaddr, i)
		curFingerTable := ws.ConstructFingers(i, rf, w)
		ByFinger(FingerId).Sort(curFingerTable)
		ws.fingers = append(ws.fingers, curFingerTable)

		//fmt.Printf("Finished ConstructFingers of server %s, layer %d\n", ws.myaddr, i)
		curSuccessorTable := ws.Successors(i, w, rs, t)
		By(RecordKey).Sort(curSuccessorTable)
		ws.succ = append(ws.succ, curSuccessorTable)

		//fmt.Printf("Finished SuccessorTable of server %s, layer %d\n", ws.myaddr, i)
	}
}

// return random Key/value record from local storage
func (ws *WhanauServer) SampleRecord(args *SampleRecordArgs, reply *SampleRecordReply) error {
	randIndex := rand.Intn(len(ws.kvstore))
	keys := make([]KeyType, 0)
	for k, _ := range ws.kvstore {
		keys = append(keys, k)
	}
	key := keys[randIndex]
	value := ws.kvstore[key]
	record := Record{key, value}

	reply.Record = record
	reply.Err = OK
	return nil
}

// Returns a list of records sampled randomly from local kv store
// Note: we agreed that duplicates are fine
func (ws *WhanauServer) SampleRecords(rd int, steps int) []Record {

	records := make([]Record, 0)
	for i := 0; i < rd; i++ {
		// random walk
		rwargs := &RandomWalkArgs{steps}
		rwreply := &RandomWalkReply{}
		counter := 0
		for rwreply.Err != OK && counter < TIMEOUT {
			ws.RandomWalk(rwargs, rwreply)
			counter++
		}
		server := rwreply.Server
		// Do rpc call to samplerecord
		srargs := &SampleRecordArgs{}
		srreply := &SampleRecordReply{}
		counter = 0
		for srreply.Err != OK && counter < TIMEOUT {
			call(server, "WhanauServer.SampleRecord", srargs, srreply)
		}

		if srreply.Err == OK {
			records = append(records, srreply.Record)
		}
	}
	return records
}

// Constructs Finger table for a specified layer
func (ws *WhanauServer) ConstructFingers(layer int, rf int, steps int) []Finger {

	DPrintf("In ConstructFingers of %s, layer %d", ws.myaddr, layer)
	fingers := make([]Finger, 0)
	for i := 0; i < rf; i++ {
		args := &RandomWalkArgs{steps}
		reply := &RandomWalkReply{}

		// Keep trying until succeed or timeout
		//counter := 0
		for reply.Err != OK {
			ws.RandomWalk(args, reply)
			//counter++
		}

		server := reply.Server

		// get id of server using rpc call to that server
		getIdArg := &GetIdArgs{layer}
		getIdReply := &GetIdReply{}
		ok := false

		// block until succeeds
		//counter = 0
		for !ok || (getIdReply.Err != OK) {
			DPrintf("rpc to getid of %s from ConstructFingers %s layer %d", server, ws.myaddr, layer)
			ok = call(server, "WhanauServer.GetId", getIdArg, getIdReply)
			//counter++
		}

		if getIdReply.Err == OK {
			finger := Finger{getIdReply.Key, server}
			fingers = append(fingers, finger)
		}
	}

	return fingers
}

// Choose id for specified layer
func (ws *WhanauServer) ChooseID(layer int) KeyType {

	DPrintf("In ChooseID of %s, layer %d", ws.myaddr, layer)
	if layer == 0 {
		// choose randomly from db
		randIndex := rand.Intn(len(ws.db))
		record := ws.db[randIndex]
		DPrintf("record.Key", record.Key)
		return record.Key

	} else {
		// choose finger randomly from layer - 1, use id of that finger
		randFinger := ws.fingers[layer-1][rand.Intn(len(ws.fingers[layer-1]))]
		return randFinger.Id
	}
}

// Defines ordering of Record args
type By func(r1, r2 *Record) bool
type ByFinger func(f1, f2 *Finger) bool

// Sort uses By to sort the Record slice
func (by By) Sort(records []Record) {
	rs := &recordSorter{
		records: records,
		by:      by,
	}
	sort.Sort(rs)
}

// sort uses By to sort fingers by id
func (by ByFinger) Sort(fingers []Finger) {
	fs := &fingerSorter{
		fingers: fingers,
		by:      by,
	}
	sort.Sort(fs)
}

// recordSorter joins a By function and a slice of Records to be sorted.
type recordSorter struct {
	records []Record
	by      func(r1, r2 *Record) bool // Closure used in the Less method.
}

// fingerSorter joins a By function and a slice of Fingers to be sorted.
type fingerSorter struct {
	fingers []Finger
	by      func(f1, f2 *Finger) bool // Closure used in the Less method
}

// Len is part of sort.Interface.
func (s *recordSorter) Len() int {
	return len(s.records)
}

func (s *fingerSorter) Len() int {
	return len(s.fingers)
}

// Swap is part of sort.Interface.
func (s *recordSorter) Swap(i, j int) {
	s.records[i], s.records[j] = s.records[j], s.records[i]
}

func (s *fingerSorter) Swap(i, j int) {
	s.fingers[i], s.fingers[j] = s.fingers[j], s.fingers[i]
}

// Less is part of sort.Interface. It is implemented by calling the "by" closure in the sorter.
func (s *recordSorter) Less(i, j int) bool {
	return s.by(&s.records[i], &s.records[j])
}

func (s *fingerSorter) Less(i, j int) bool {
	return s.by(&s.fingers[i], &s.fingers[j])
}

var RecordKey = func(r1, r2 *Record) bool {
	return r1.Key < r2.Key
}

var FingerId = func(f1, f2 *Finger) bool {
	return f1.Id < f2.Id
}

// Gets successors that are nearest each key
func (ws *WhanauServer) SampleSuccessors(args *SampleSuccessorsArgs, reply *SampleSuccessorsReply) error {
	By(RecordKey).Sort(ws.db)

	key := args.Key
	t := args.T
	var records []Record
	curCount := 0
	curRecord := 0
	if t <= len(ws.db) {
		for curCount < t {
			if ws.db[curRecord].Key >= key {
				records = append(records, ws.db[curRecord])
				curCount++
			}
			curRecord++
			if curRecord == len(ws.db) {
				curRecord = 0
				key = ws.db[curRecord].Key
			}
		}
		reply.Successors = records
		reply.Err = OK
	} else {
		reply.Err = ErrNoKey
	}
	return nil
}

func (ws *WhanauServer) Successors(layer int, steps int, rs int, nsuccessors int) []Record {
	DPrintf("In Sucessors of %s, layer %d", ws.myaddr, layer)
	var successors []Record
	for i := 0; i < rs; i++ {
		args := &RandomWalkArgs{}
		args.Steps = steps
		reply := &RandomWalkReply{}
		ws.RandomWalk(args, reply)

		if reply.Err == OK {
			vj := reply.Server
			getIdArgs := &GetIdArgs{layer}
			getIdReply := &GetIdReply{}
			DPrintf("Calling getid layer: %d in Successors of %s", layer, ws.myaddr)
			ws.GetId(getIdArgs, getIdReply)

			sampleSuccessorsArgs := &SampleSuccessorsArgs{getIdReply.Key, nsuccessors}
			sampleSuccessorsReply := &SampleSuccessorsReply{}
			for sampleSuccessorsReply.Err != OK {
				call(vj, "WhanauServer.SampleSuccessors", sampleSuccessorsArgs, sampleSuccessorsReply)
			}
			successors = append(successors, sampleSuccessorsReply.Successors...)
		}
	}
	return successors
}


// each server changes its current state, and sends the setup messages
// to all of its neighbors
func (ws *WhanauServer) StartSetup(args *StartSetupArgs, reply *StartSetupReply) error {
	ws.mu.Lock()
	if (ws.state == Normal) {
		ws.state = PreSetup
	}
	ws.mu.Unlock()

	// forward this msg to all of its neighbors
	for _, srv := range ws.neighbors {
		rpc_args := &StartSetupArgs{args.MasterServer}
		rpc_reply := &StartSetupReply{}
		ok := call(srv, "WhanauServer.StartSetup", rpc_args, rpc_reply)
		if ok {
			
		}
	}
	
	if (ws.state == PreSetup) {
		// send its pending keys to one of the random master nodes
		for {
			randIndex := rand.Intn(len(ws.master))
			master_server := ws.master[randIndex]
			rpc_args := &ReceivePendingWritesArgs{ws.pending}
			rpc_reply := &ReceivePendingWritesReply{}
			ok := call(master_server, "WhanauServer.ReceivePendingWrites", rpc_args, rpc_reply)
			if ok {
				if rpc_reply.Err == OK {
					ws.mu.Lock()
					ws.state = Setup
					ws.mu.Unlock()
					break
				}
			}
		}
	}
	return nil
}

func (ws *WhanauServer) ReceivePendingWrites(args *ReceivePendingWritesArgs, reply *ReceivePendingWritesReply) error {
	// store the pending writes to its own pending
	ws.mu.Lock()
	for k, v := range args.PendingWrites {
		ws.pending[k] = v
	}
	ws.mu.Unlock()
	reply.Err = OK
	return nil
}


// this function shoud be run in a separate thread 
// by each master server
func (ws *WhanauServer) InitiateSetup() {
	for {
		if (ws.state == Normal) {
			// send a start setup message to each one of its neighbors
			for _, srv := range ws.neighbors {
				args := &StartSetupArgs{ws.myaddr}
				reply := &StartSetupReply{}
				ok := call(srv, "WhanauServer.StartSetup", args, reply)
				if ok {
					//pending_writes := reply.PendingWrites
				}
			}
		}

		time.Sleep(180 * time.Second)
	}
}

// tell the server to shut itself down.
func (ws *WhanauServer) Kill() {
	ws.dead = true
	ws.l.Close()
	//	ws.px.Kill()
}

// TODO servers is for a paxos cluster
func StartServer(servers []string, me int, myaddr string, neighbors []string) *WhanauServer {
	ws := new(WhanauServer)
	ws.me = me
	ws.myaddr = myaddr
	ws.neighbors = neighbors

	ws.kvstore = make(map[KeyType]ValueType)
	ws.pkvstore = make(map[KeyType]TrueValueType)
	ws.state = Normal
	ws.reqID = 0

	rpcs := rpc.NewServer()
	rpcs.Register(ws)

	os.Remove(servers[me])
	l, e := net.Listen("unix", servers[me])
	if e != nil {
		log.Fatal("listen error: ", e)
	}
	ws.l = l

	go func() {
		for ws.dead == false {
			conn, err := ws.l.Accept()
			// removed unreliable code for now
			if err == nil && ws.dead == false {
				go rpcs.ServeConn(conn)
			} else if err == nil {
				conn.Close()
			}

			if err != nil && ws.dead == false {
				fmt.Printf("ShardWS(%v) accept: %v\n", me, err.Error())
				ws.Kill()
			}
		}
	}()

	// removed tick() loop for now

	return ws
}

// Methods used only for testing

// This method is only used for putting ids into the table for testing purposes
func (ws *WhanauServer) PutId(args *PutIdArgs, reply *PutIdReply) error {
	//ws.ids[args.Layer] = args.Key
	ws.ids = append(ws.ids, args.Key)
	reply.Err = OK
	return nil
}
