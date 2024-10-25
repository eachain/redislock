package redislock

import (
	"context"
	"crypto/rand"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	mrand "math/rand"
	"sync/atomic"
	"time"
)

const (
	// function Extend(key, value, expireSeconds)
	luaExtend = `local val = redis.call("get",KEYS[1])
if val and val ~= ARGV[1] then return val end
return redis.call("set",KEYS[1],ARGV[1],"EX",ARGV[2])`

	// function Unlock(key, value)
	luaUnlock = `local val = redis.call("get",KEYS[1])
if not val then return "OK" end
if val ~= ARGV[1] then return val end
redis.call("del",KEYS[1])
return "OK"`
)

type RedisClient interface {
	SetNX(ctx context.Context, key string, value any, expireSeconds int64) (bool, error)
	Eval(ctx context.Context, script string, keys []string, args ...any) (any, error)
}

var (
	ErrLockFailed = errors.New("redis lock: lock failed")
	ErrLockLost   = errors.New("redis lock: lock lost")
)

type mutex struct {
	redis RedisClient
	key   string
	value string
}

func newMutex(redis RedisClient, key string) *mutex {
	return &mutex{
		redis: redis,
		key:   key,
		value: randMutexValue(),
	}
}

func (mut *mutex) Lock(ctx context.Context, expireSeconds int64) error {
	ok, err := mut.redis.SetNX(ctx, mut.key, mut.value, expireSeconds)
	if err != nil {
		return err
	}
	if !ok {
		return ErrLockFailed
	}
	return nil
}

func (mut *mutex) Extend(ctx context.Context, ex int64) error {
	result, err := mut.redis.Eval(ctx, luaExtend, []string{mut.key}, mut.value, ex)
	if err != nil {
		return err
	}
	value, ok := result.(string)
	if !ok {
		return fmt.Errorf("redis mutex: lua eval Extend result type is not string: %T", result)
	}
	if value != "OK" {
		return ErrLockLost
	}
	return nil
}

func (mut *mutex) Unlock(ctx context.Context) error {
	result, err := mut.redis.Eval(ctx, luaUnlock, []string{mut.key}, mut.value)
	if err != nil {
		return err
	}
	value, ok := result.(string)
	if !ok {
		return fmt.Errorf("redis lock: lua eval Unlock result type is not string: %T", result)
	}
	if value != "OK" {
		return ErrLockLost
	}
	return nil
}

// Mutex is a distributed lock implemented with redis.
type Mutex struct {
	mut    *mutex
	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}
	valid  int32
}

// NewMutex returns a new distributed lock.
// redis client can be github.com/go-redis/redis.Client or other redis clients.
func NewMutex(redis RedisClient, key string) *Mutex {
	return &Mutex{
		mut: newMutex(redis, key),
	}
}

// TryLock try handle a lock with expireSeconds.
// It fails after ctx.Done().
// If TryLock returns nil, namely lock successfully,
// *Mutex will auto extend the lock until Unlock is called.
func (mut *Mutex) TryLock(ctx context.Context, expireSeconds int64) error {
	for {
		err := mut.mut.Lock(ctx, expireSeconds)
		if err == nil {
			mut.autoExtend(expireSeconds)
			return nil
		}
		if err != ErrLockFailed {
			return err
		}

		rnd := mrand.New(mrand.NewSource(time.Now().UnixNano()))
		ms := rnd.Intn(101) + 100
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Duration(ms) * time.Millisecond):
			// retry
		}
	}
}

func (mut *Mutex) autoExtend(expireSeconds int64) {
	atomic.StoreInt32(&mut.valid, 1)
	mut.done = make(chan struct{})
	mut.ctx, mut.cancel = context.WithCancel(context.Background())

	go func() {
		defer close(mut.done)
		intv := time.Duration(expireSeconds) * time.Second * 3 / 7

		for {
			select {
			case <-mut.ctx.Done():
				return
			case <-time.After(intv):
			}

			err := mut.mut.Extend(mut.ctx, expireSeconds)
			if err == nil {
				atomic.StoreInt32(&mut.valid, 1)
			} else {
				atomic.StoreInt32(&mut.valid, 0)
			}
		}
	}()
}

// Valid reports whether the distributed lock is valid.
func (mut *Mutex) Valid() bool {
	return atomic.LoadInt32(&mut.valid) == 1
}

// Unlock unlock the distributed lock.
func (mut *Mutex) Unlock(ctx context.Context) error {
	atomic.StoreInt32(&mut.valid, 0)
	if mut.cancel != nil {
		mut.cancel()
		mut.cancel = nil
	}
	if mut.done != nil {
		<-mut.done
		mut.done = nil
	}
	return mut.mut.Unlock(ctx)
}

func randMutexValue() string {
	var b [1024]byte
	rand.Read(b[:])
	sum := sha1.Sum(b[:])
	return hex.EncodeToString(sum[:])
}
