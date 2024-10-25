package redislock

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type item struct {
	value    any
	expireAt time.Time
}

type mockRedis struct {
	mut  sync.Mutex
	item map[string]*item
}

func (r *mockRedis) SetNX(ctx context.Context, key string, value any, expireSeconds int64) (bool, error) {
	r.mut.Lock()
	defer r.mut.Unlock()

	it := r.item[key]
	if it != nil && it.expireAt.After(time.Now()) {
		return false, nil
	}

	if it == nil {
		it = new(item)
		if r.item == nil {
			r.item = make(map[string]*item)
		}
		r.item[key] = it
	}
	it.value = value
	it.expireAt = time.Now().Add(time.Duration(expireSeconds) * time.Second)
	return true, nil
}

func (r *mockRedis) Eval(ctx context.Context, script string, keys []string, args ...any) (any, error) {
	switch script {
	case luaExtend:
		return r.extend(keys[0], args[0], args[1].(int64))
	case luaUnlock:
		return r.unlock(keys[0], args[0])
	default:
		return nil, errors.New("mock redis: unsupported script")
	}
}

func (r *mockRedis) getWithLock(key string) any {
	if it := r.item[key]; it != nil && it.expireAt.After(time.Now()) {
		return it.value
	}
	return nil
}

func (r *mockRedis) setEXWithLock(key string, value any, expireSeconds int64) {
	it := r.item[key]
	if it == nil {
		it = new(item)
		if r.item == nil {
			r.item = make(map[string]*item)
		}
		r.item[key] = it
	}
	it.value = value
	it.expireAt = time.Now().Add(time.Duration(expireSeconds) * time.Second)
}

func (r *mockRedis) setEX(key string, value any, expireSeconds int64) {
	r.mut.Lock()
	defer r.mut.Unlock()
	r.setEXWithLock(key, value, expireSeconds)
}

func (r *mockRedis) extend(key string, value any, expireSeconds int64) (any, error) {
	r.mut.Lock()
	defer r.mut.Unlock()

	stored := r.getWithLock(key)
	if stored != nil && stored != value {
		return stored, nil
	}
	r.setEXWithLock(key, value, expireSeconds)
	return "OK", nil
}

func (r *mockRedis) delWithLock(key string) {
	delete(r.item, key)
}

func (r *mockRedis) del(key string) {
	r.mut.Lock()
	defer r.mut.Unlock()
	r.delWithLock(key)
}

func (r *mockRedis) unlock(key string, value any) (any, error) {
	r.mut.Lock()
	defer r.mut.Unlock()

	stored := r.getWithLock(key)
	if stored == nil {
		return "OK", nil
	}
	if stored != value {
		return stored, nil
	}
	r.delWithLock(key)
	return "OK", nil
}

func TestTryLock(t *testing.T) {
	redis := new(mockRedis)
	mut1 := NewMutex(redis, "mutex:key")
	err := mut1.TryLock(context.Background(), 3)
	if err != nil {
		t.Fatalf("mut1 TryLock: %v", err)
	}
	defer mut1.Unlock(context.Background())

	mut2 := NewMutex(redis, "mutex:key")
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	err = mut2.TryLock(ctx, 3)
	if err == nil {
		mut2.Unlock(context.Background())
		t.Fatalf("mut2 TryLock success")
	}
}

func TestValid(t *testing.T) {
	redis := new(mockRedis)
	mut := NewMutex(redis, "mutex:key")
	err := mut.TryLock(context.Background(), 3)
	if err != nil {
		t.Fatalf("mut TryLock: %v", err)
	}
	defer mut.Unlock(context.Background())

	if !mut.Valid() {
		t.Fatalf("mut valid: false")
	}

	mut.Unlock(context.Background())
	if mut.Valid() {
		t.Fatalf("mut valid after unlock")
	}
}

func TestUnlock(t *testing.T) {
	redis := new(mockRedis)
	mut1 := NewMutex(redis, "mutex:key")
	err := mut1.TryLock(context.Background(), 3)
	if err != nil {
		t.Fatalf("mut1 TryLock: %v", err)
	}

	if err := mut1.Unlock(context.Background()); err != nil {
		t.Fatalf("mut1 first Unlock: %v", err)
	}

	if err := mut1.Unlock(context.Background()); err != nil {
		t.Fatalf("mut1 second Unlock: %v", err)
	}

	mut2 := NewMutex(redis, "mutex:key")
	mut2.TryLock(context.Background(), 3)
	defer mut2.Unlock(context.Background())

	if err := mut1.Unlock(context.Background()); err != ErrLockLost {
		t.Fatalf("mut1 third Unlock: %v", err)
	}
}

func TestAutoExtend(t *testing.T) {
	redis := new(mockRedis)
	mut := NewMutex(redis, "mutex:key")
	err := mut.TryLock(context.Background(), 1)
	if err != nil {
		t.Fatalf("mut TryLock: %v", err)
	}
	defer mut.Unlock(context.Background())

	if !mut.Valid() {
		t.Fatalf("mut valid: false")
	}

	redis.del("mutex:key")
	time.Sleep(time.Second + 500*time.Millisecond)
	if !mut.Valid() {
		t.Fatalf("mut invalid after 1.5s")
	}

	redis.setEX("mutex:key", "some other value", 3)
	time.Sleep(time.Second + 500*time.Millisecond)
	if mut.Valid() {
		t.Fatalf("mut valid after set other value")
	}
}
