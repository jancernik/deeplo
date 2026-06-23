package runner

import (
	"context"
	"sync"
)

type KeyedMutex struct {
	mutex sync.Mutex
	keys  map[string]chan struct{}
}

func NewKeyedMutex() *KeyedMutex {
	return &KeyedMutex{keys: make(map[string]chan struct{})}
}

func (keyedMutex *KeyedMutex) Lock(ctx context.Context, key string) (unlock func(), err error) {
	channel := keyedMutex.getOrCreate(key)
	select {
	case <-channel:
		return func() { channel <- struct{}{} }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (keyedMutex *KeyedMutex) getOrCreate(key string) chan struct{} {
	keyedMutex.mutex.Lock()
	defer keyedMutex.mutex.Unlock()
	channel, ok := keyedMutex.keys[key]
	if !ok {
		channel = make(chan struct{}, 1)
		channel <- struct{}{}
		keyedMutex.keys[key] = channel
	}
	return channel
}
