package consumer

import (
	"github.com/apache/rocketmq-client-go/v2/primitive"
	"sync"
	"testing"
)

func TestLocalCacheReadersSynchronizeWithPut(t *testing.T) {
	pq := newProcessQueue(true) // avoid dispatching to msgCh; exercise the same shared cache.
	pc := &pushConsumer{}
	pc.defaultConsumer = &defaultConsumer{option: defaultPushConsumerOptions()}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for n := 0; n < 10000; n++ {
			m := &primitive.MessageExt{QueueOffset: int64(n % 64)}
			pq.putMessage(m)
			pq.removeMessage(m)
		}
	}()
	go func() {
		defer wg.Done()
		for n := 0; n < 10000; n++ {
			pq.cleanExpiredMsg(pc)
		}
	}()
	wg.Wait()
}

func TestLocalTakeMessagesSynchronizesWithPut(t *testing.T) {
	pq := newProcessQueue(true)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for n := 0; n < 1000; n++ {
			pq.putMessage(&primitive.MessageExt{QueueOffset: int64(n)})
		}
	}()
	go func() {
		defer wg.Done()
		for n := 0; n < 1000; n++ {
			pq.takeMessages(1)
			pq.commit()
		}
	}()
	wg.Wait()
}
