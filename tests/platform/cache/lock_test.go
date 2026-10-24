package cache_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"bgin/internal/platform/cache"
)

func TestMemoryLocker_Exclusive(t *testing.T) {
	t.Parallel()

	locker := cache.NewMemoryLocker()
	defer func() { _ = locker.Close() }()

	const lockName = "test:order:1001"
	var executionOrder []int
	var mu sync.Mutex

	var wg sync.WaitGroup
	wg.Add(2)

	// 协程 1
	go func() {
		defer wg.Done()
		lockedCtx, cancel, err := locker.Lock(context.Background(), lockName)
		assert.NoError(t, err)
		defer cancel()

		mu.Lock()
		executionOrder = append(executionOrder, 1)
		mu.Unlock()

		time.Sleep(30 * time.Millisecond)
		_ = lockedCtx
	}()

	// 稍作等待以确保协程 1 先抢占
	time.Sleep(5 * time.Millisecond)

	// 协程 2
	go func() {
		defer wg.Done()
		lockedCtx, cancel, err := locker.Lock(context.Background(), lockName)
		assert.NoError(t, err)
		defer cancel()

		mu.Lock()
		executionOrder = append(executionOrder, 2)
		mu.Unlock()
		_ = lockedCtx
	}()

	wg.Wait()

	mu.Lock()
	assert.Equal(t, []int{1, 2}, executionOrder, "锁应确保按严格互斥顺序执行")
	mu.Unlock()
}

func TestMemoryLocker_TryLock_Contention(t *testing.T) {
	t.Parallel()

	locker := cache.NewMemoryLocker()
	defer func() { _ = locker.Close() }()

	const lockName = "test:account:2001"

	// 第一次 TryLock 应成功
	ctx1, cancel1, err1 := locker.TryLock(context.Background(), lockName)
	require.NoError(t, err1)
	require.NotNil(t, ctx1)

	// 第二次 TryLock 应被拒绝并返回 ErrNotLocked
	ctx2, cancel2, err2 := locker.TryLock(context.Background(), lockName)
	require.ErrorIs(t, err2, cache.ErrNotLocked)
	assert.Nil(t, ctx2)
	assert.Nil(t, cancel2)

	// 释放第一次的锁
	cancel1()

	// 再次 TryLock 应重获成功
	ctx3, cancel3, err3 := locker.TryLock(context.Background(), lockName)
	require.NoError(t, err3)
	require.NotNil(t, ctx3)
	cancel3()
}

func TestMemoryLocker_DoWithLock(t *testing.T) {
	t.Parallel()

	store := cache.NewMemoryStore()
	defer func() { _ = store.Close() }()

	locker := store.Locker()
	defer func() { _ = locker.Close() }()

	var executed bool
	expectedErr := errors.New("business error inside lock")

	err := cache.DoWithLock(context.Background(), locker, "test:settle", func(ctx context.Context) error {
		executed = true
		assert.NotNil(t, ctx)
		return expectedErr
	})

	assert.True(t, executed)
	require.ErrorIs(t, err, expectedErr)

	// 验证退出后锁已自动释放，可以立即重新获取
	errReacquired := cache.DoWithLock(context.Background(), locker, "test:settle", func(ctx context.Context) error {
		return nil
	})
	require.NoError(t, errReacquired)
}

func TestMemoryLocker_DoTryLock(t *testing.T) {
	t.Parallel()

	locker := cache.NewMemoryLocker()
	defer func() { _ = locker.Close() }()

	var ops atomic.Int32
	holdStarted := make(chan struct{})
	releaseHold := make(chan struct{})

	go func() {
		_ = cache.DoTryLock(context.Background(), locker, "test:concurrent", func(ctx context.Context) error {
			ops.Add(1)
			close(holdStarted)
			<-releaseHold
			return nil
		})
	}()

	<-holdStarted

	// 并发抢锁应直接失败
	errConflict := cache.DoTryLock(context.Background(), locker, "test:concurrent", func(ctx context.Context) error {
		ops.Add(1)
		return nil
	})
	require.ErrorIs(t, errConflict, cache.ErrNotLocked)

	// 释放第一个持锁协程
	close(releaseHold)
	time.Sleep(10 * time.Millisecond)

	// 再次抢锁应成功
	errSuccess := cache.DoTryLock(context.Background(), locker, "test:concurrent", func(ctx context.Context) error {
		ops.Add(1)
		return nil
	})
	require.NoError(t, errSuccess)
	assert.Equal(t, int32(2), ops.Load())
}

func TestMemoryLocker_ContextCancel(t *testing.T) {
	t.Parallel()

	locker := cache.NewMemoryLocker()
	defer func() { _ = locker.Close() }()

	// 先占有锁
	_, cancel, err := locker.Lock(context.Background(), "test:ctx:cancel")
	require.NoError(t, err)
	defer cancel()

	// 携带超时的 context 尝试获取
	timeoutCtx, timeoutCancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer timeoutCancel()

	_, _, waitErr := locker.Lock(timeoutCtx, "test:ctx:cancel")
	require.ErrorIs(t, waitErr, context.DeadlineExceeded)
}

func TestMemoryLocker_Closed(t *testing.T) {
	t.Parallel()

	locker := cache.NewMemoryLocker()
	err := locker.Close()
	require.NoError(t, err)

	_, _, errLock := locker.Lock(context.Background(), "test:closed")
	require.ErrorIs(t, errLock, cache.ErrLockerClosed)

	_, _, errTry := locker.TryLock(context.Background(), "test:closed")
	require.ErrorIs(t, errTry, cache.ErrLockerClosed)
}
