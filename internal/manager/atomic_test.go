package manager

import "sync/atomic"

func atomicAdd(p *int32, d int32) int32 { return atomic.AddInt32(p, d) }
func atomicLoad(p *int32) int32         { return atomic.LoadInt32(p) }
func atomicStore(p *int32, v int32)     { atomic.StoreInt32(p, v) }
