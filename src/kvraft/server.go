package kvraft

import (
	"6.5840/labgob"
	"6.5840/labrpc"
	"6.5840/raft"
	"sync"
	"sync/atomic"
)

type OpType int

const (
	GetType OpType = iota
	AppendType
	PutType
	DeleteType
)

type Op struct {
	ID    string
	Type  OpType
	Key   string
	Value string
}

type KVServer struct {
	mu      sync.Mutex
	me      int
	rf      *raft.Raft
	applyCh chan raft.ApplyMsg
	dead    int32 // set by Kill()

	maxraftstate int // snapshot if log grows this big

	data             map[string]string // state machine data
	executed         map[string]bool   // executed requestId
	versionData      map[string]string // requestID to data
	lastAppliedIndex int
	cond             *sync.Cond      // condition variale
	persiter         *raft.Persister // persist util
}

func (kv *KVServer) lock() {
	kv.mu.Lock()
}

func (kv *KVServer) unlock() {
	kv.mu.Unlock()
}

func (kv *KVServer) isLeader() bool {
	_, ret := kv.rf.GetState()
	return ret
}

func (kv *KVServer) checkExecuted(id string) bool {
	if _, ok := kv.executed[id]; ok {
		return true
	}
	return false
}

func (kv *KVServer) Get(req *GetArgs, resp *GetReply) {
	if kv.killed() {
		return
	}
	kv.lock()
	defer kv.unlock()

	m := GetMethod
	if !kv.isLeader() {
		debugf(m, kv.me, "not leader, req: %v", toJson(req))
		resp.Err = ErrWrongLeader
		return
	}
	resp.Err = OK
	if kv.checkExecuted(req.ID) {
		if _, ok := kv.versionData[req.ID]; !ok {
			panic(req.ID)
		}
		resp.Value = kv.versionData[req.ID]
		debugf(m, kv.me, "Executed, id: %v", req.ID)
		return
	}

	op := Op{
		ID:   req.ID,
		Key:  req.Key,
		Type: GetType,
	}

	index, term, isLeader := kv.rf.Start(op)
	if !isLeader {
		debugf(m, kv.me, "not leader, req: %v", toJson(req))
		resp.Err = ErrWrongLeader
		return
	}
	debugf(m, kv.me, "req: %v, index: %v, term:%v", toJson(req), index, term)

	timeoutChan := make(chan bool, 1)
	go startTimeout(kv.cond, timeoutChan)
	timeout := false
	for !(index <= kv.lastAppliedIndex) && !timeout {
		select {
		case <-timeoutChan:
			timeout = true
		default:
			kv.cond.Wait() // wait, must hold mutex, after blocked, release lock
		}
	}
	if !kv.isLeader() {
		debugf(m, kv.me, "not leader, req: %v", toJson(req))
		resp.Err = ErrWrongLeader
		return
	}
	if timeout {
		resp.Err = ErrTimeout
		debugf(m, kv.me, "timeout!, req: %v", toJson(req))
		return
	}
	if _, ok := kv.versionData[op.ID]; !ok {
		panic("empty")
	}
	res := kv.versionData[op.ID]
	if res == "" {
		debugf(m, kv.me, "warning, req:%v", toJson(req))
	}
	debugf(m, kv.me, "success, req: %v, value: %v", toJson(req), res)
	resp.Value = res
}

func (kv *KVServer) Put(req *PutAppendArgs, resp *PutAppendReply) {
	// Your code here.
	if kv.killed() {
		return
	}
	kv.lock()
	defer kv.unlock()
	m := PutMethod
	if !kv.isLeader() {
		debugf(m, kv.me, "not leader, req: %v", toJson(req))
		resp.Err = ErrWrongLeader
		return
	}

	resp.Err = OK
	if kv.checkExecuted(req.ID) {
		debugf(m, kv.me, "Executed, id: %v", toJson(req))
		return
	}

	op := Op{
		ID:    req.ID,
		Type:  PutType,
		Key:   req.Key,
		Value: req.Value,
	}
	debugf(m, kv.me, "start, id: %v", req.ID)
	index, term, isLeader := kv.rf.Start(op)
	if !isLeader {
		debugf(m, kv.me, "not leader, req: %v", toJson(req))
		resp.Err = ErrWrongLeader
		return
	}
	debugf(m, kv.me, "req: %v, index: %v, term:%v", toJson(req), index, term)
	timeoutChan := make(chan bool, 1)
	go startTimeout(kv.cond, timeoutChan)
	timeout := false
	for !(index <= kv.lastAppliedIndex) && !timeout {
		select {
		case <-timeoutChan:
			timeout = true // timeout notify, raft can not do a agreement
		default:
			kv.cond.Wait() // wait, must hold mutex, after blocked, release lock
		}
	}
	if !kv.isLeader() {
		debugf(m, kv.me, "not leader, req: %v", toJson(req))
		resp.Err = ErrWrongLeader
		return
	}
	if timeout {
		resp.Err = ErrTimeout
		debugf(m, kv.me, "timeout!, req: %v", toJson(req))
		return
	}
	debugf(m, kv.me, "success, req:%v", toJson(req))
}

func (kv *KVServer) Append(req *PutAppendArgs, resp *PutAppendReply) {
	if kv.killed() {
		return
	}
	kv.lock()
	defer kv.unlock()
	meth := AppendMethod
	if !kv.isLeader() {
		debugf(meth, kv.me, "not leader, req: %v", toJson(req))
		resp.Err = ErrWrongLeader
		return
	}
	resp.Err = OK
	if kv.checkExecuted(req.ID) {
		debugf(meth, kv.me, "Executed, id: %v", req.ID)
		return
	}

	debugf(meth, kv.me, "arrive req: %v", toJson(req))
	op := Op{
		ID:    req.ID,
		Type:  AppendType,
		Key:   req.Key,
		Value: req.Value,
	}
	debugf(meth, kv.me, "start id: %v", req.ID)
	index, term, isLeader := kv.rf.Start(op)
	if !isLeader {
		debugf(meth, kv.me, "not leader, req: %v", toJson(req))
		resp.Err = ErrWrongLeader
		return
	}
	debugf(meth, kv.me, "req: %v, index: %v, term:%v", toJson(req), index, term)

	timeoutChan := make(chan bool, 1)
	go startTimeout(kv.cond, timeoutChan) // boot a goroutine to finish timeout signal
	timeout := false
	for !(index <= kv.lastAppliedIndex) && !timeout {
		select {
		case <-timeoutChan:
			timeout = true // timeout notify, raft can not do a agreement
			debugf(meth, kv.me, "timeout!, req: %v", toJson(req))
		default:
			kv.cond.Wait() // wait, must hold mutex, after blocked, release lock
		}
	}
	if !kv.isLeader() {
		debugf(meth, kv.me, "not leader, req: %v", toJson(req))
		resp.Err = ErrWrongLeader
		return
	}
	if timeout {
		resp.Err = ErrTimeout
		debugf(meth, kv.me, "timeout!, req: %v", toJson(req))
		return
	}
	debugf(meth, kv.me, "success, req: %v", toJson(req))
	return
}

func (kv *KVServer) Notify(req *NotifyFinishedRequest, resp *NotifyFinishedResponse) {
	if kv.killed() {
		return
	}
	kv.lock()
	defer kv.unlock()
	m := Notify
	if !kv.isLeader() {
		debugf(m, kv.me, "not leader, req: %v", toJson(req))
		resp.Err = ErrWrongLeader
		return
	}
	resp.Err = OK

	op := Op{
		Type: DeleteType,
		Key:  req.ID,
	}
	debugf(m, kv.me, "start delete: %v", req.ID)
	index, term, isLeader := kv.rf.Start(op)
	if !isLeader {
		debugf(m, kv.me, "not leader, req: %v", toJson(req))
		resp.Err = ErrWrongLeader
		return
	}
	debugf(m, kv.me, "req: %v, index: %v, term:%v", toJson(req), index, term)
	timeoutChan := make(chan bool, 1)
	go startTimeout(kv.cond, timeoutChan)
	timeout := false
	for !(index <= kv.lastAppliedIndex) && !timeout {
		select {
		case <-timeoutChan:
			timeout = true // timeout notify, raft can not do a agreement
		default:
			kv.cond.Wait() // wait, must hold mutex, after blocked, release lock
		}
	}
	if !kv.isLeader() {
		debugf(m, kv.me, "not leader, req: %v", toJson(req))
		resp.Err = ErrWrongLeader
		return
	}
	if timeout {
		resp.Err = ErrTimeout
		debugf(m, kv.me, "timeout!, req: %v", toJson(req))
		return
	}
	debugf(m, kv.me, "success, req:%v", toJson(req))
}

// the tester calls Kill() when a KVServer instance won't
// be needed again. for your convenience, we supply
// code to set rf.dead (without needing a lock),
// and a killed() method to test rf.dead in
// long-running loops. you can also add your own
// code to Kill(). you're not required to do anything
// about this, but it may be convenient (for example)
// to suppress debug output from a Kill()ed instance.
func (kv *KVServer) Kill() {
	atomic.StoreInt32(&kv.dead, 1)
	kv.rf.Kill()
	debugf(KILL, kv.me, "be killed")
}

func (kv *KVServer) killed() bool {
	z := atomic.LoadInt32(&kv.dead)
	return z == 1
}

// servers[] contains the ports of the set of
// servers that will cooperate via Raft to
// form the fault-tolerant key/value service.
// me is the index of the current server in servers[].
// the k/v server should store snapshots through the underlying Raft
// implementation, which should call persister.SaveStateAndSnapshot() to
// atomically save the Raft state along with the snapshot.
// the k/v server should snapshot when Raft's saved state exceeds maxraftstate bytes,
// in order to allow Raft to garbage-collect its log. if maxraftstate is -1,
// you don't need to snapshot.
// StartKVServer() must return quickly, so it should start goroutines
// for any long-running work.
func StartKVServer(servers []*labrpc.ClientEnd, me int, persister *raft.Persister, maxraftstate int) *KVServer {
	// call labgob.Register on structures you want
	// Go's RPC library to marshall/unmarshall.
	labgob.Register(Op{})
	kv := new(KVServer)
	kv.me = me
	kv.maxraftstate = maxraftstate

	kv.data = map[string]string{}
	kv.versionData = map[string]string{}
	kv.executed = map[string]bool{}
	kv.applyCh = make(chan raft.ApplyMsg, 100)
	kv.persiter = persister
	kv.lastAppliedIndex = 0
	kv.applySnapshot(persister.ReadSnapshot())
	kv.cond = sync.NewCond(&kv.mu)
	debugf(Start, kv.me, "data: %v, executed: %v, versionData:%v", toJson(kv.data), toJson(kv.executed), toJson(kv.versionData))
	kv.rf = raft.Make(servers, me, persister, kv.applyCh)

	go kv.applyMsgForStateMachine()
	go kv.dectionMaxSize()
	return kv
}