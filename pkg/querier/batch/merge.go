// SPDX-License-Identifier: AGPL-3.0-only
// Provenance-includes-location: https://github.com/cortexproject/cortex/blob/master/pkg/querier/batch/merge.go
// Provenance-includes-license: Apache-2.0
// Provenance-includes-copyright: The Cortex Authors.

package batch

import (
	"container/heap"
	"sort"

	"github.com/prometheus/prometheus/model/histogram"
	"github.com/prometheus/prometheus/tsdb/chunkenc"
	"github.com/prometheus/prometheus/util/zeropool"

	"github.com/grafana/mimir/pkg/storage/chunk"
)

type mergeIterator struct {
	its []*nonOverlappingIterator
	h   iteratorHeap

	// Store the current sorted batchStream
	batches batchStream

	// Buffers to merge in.
	batchesBuf   batchStream
	hPool        zeropool.Pool[*histogram.Histogram]
	fhPool       zeropool.Pool[*histogram.FloatHistogram]

	currErr error
}

func newMergeIterator(it iterator, cs []GenericChunk) *mergeIterator {
	c, ok := it.(*mergeIterator)
	if ok {
		c.currErr = nil
	} else {
		c = &mergeIterator{}
		c.hPool = zeropool.New(func() *histogram.Histogram { return &histogram.Histogram{} })
		c.fhPool = zeropool.New(func() *histogram.FloatHistogram { return &histogram.FloatHistogram{} })
	}

	css := partitionChunks(cs)
	if cap(c.its) >= len(css) {
		c.its = c.its[:len(css)]
		c.h = c.h[:0]
		c.batches = c.batches[:0]
		// We are not resetting the content of c.batchesBuf because they will be
		// reset once we call mergeStreams() on them.
		c.batchesBuf = c.batchesBuf[:len(css)]
	} else {
		c.its = make([]*nonOverlappingIterator, len(css))
		c.h = make(iteratorHeap, 0, len(c.its))
		c.batches = make(batchStream, 0, len(c.its))
		c.batchesBuf = make(batchStream, len(c.its))
	}
	for i, cs := range css {
		c.its[i] = newNonOverlappingIterator(c.its[i], cs)
	}

	for _, iter := range c.its {
		if iter.Next(1) != chunkenc.ValNone {
			c.h = append(c.h, iter)
			continue
		}

		if err := iter.Err(); err != nil {
			c.currErr = err
		}
	}

	heap.Init(&c.h)
	return c
}

func (c *mergeIterator) putPointerValuesToThePool(b chunk.Batch) {
	if b.ValueType == chunkenc.ValHistogram {
		for i := 0; i < b.Length; i++ {
			c.hPool.Put(b.Histograms[i])
		}
	} else if b.ValueType == chunkenc.ValFloatHistogram {
		for i := 0; i < b.Length; i++ {
			c.fhPool.Put(b.FloatHistograms[i])
		}
	}
}

func (c *mergeIterator) Seek(t int64, size int) chunkenc.ValueType {

	// Optimisation to see if the seek is within our current caches batches.
found:
	for len(c.batches) > 0 {
		batch := &c.batches[0]
		if t >= batch.Timestamps[0] && t <= batch.Timestamps[batch.Length-1] {
			batch.Index = 0
			for batch.Index < batch.Length && t > batch.Timestamps[batch.Index] {
				batch.Index++
			}
			break found
		}
		// The first batch is not needed anymore, so we put pointers to its Histograms/FloatHistograms
		// to the pool in order to reuse them.
		c.putPointerValuesToThePool(c.batches[0])
		copy(c.batches, c.batches[1:])
		c.batches = c.batches[:len(c.batches)-1]
	}

	// If we didn't find anything in the current set of batches, reset the heap
	// and seek.
	if len(c.batches) == 0 {
		c.h = c.h[:0]
		c.batches = c.batches[:0]

		for _, iter := range c.its {
			if iter.Seek(t, size) != chunkenc.ValNone {
				c.h = append(c.h, iter)
				continue
			}

			if err := iter.Err(); err != nil {
				c.currErr = err
				return chunkenc.ValNone
			}
		}

		heap.Init(&c.h)
	}

	return c.buildNextBatch(size)
}

func (c *mergeIterator) Next(size int) chunkenc.ValueType {
	// Pop the last built batch in a way that doesn't extend the slice.
	if len(c.batches) > 0 {
		// The first batch is not needed anymore, so we put pointers to its Histograms/FloatHistograms
		// to the pool in order to reuse them.
		c.putPointerValuesToThePool(c.batches[0])
		copy(c.batches, c.batches[1:])
		c.batches = c.batches[:len(c.batches)-1]
	}

	return c.buildNextBatch(size)
}

func (c *mergeIterator) nextBatchEndTime() int64 {
	batch := &c.batches[0]
	return batch.Timestamps[batch.Length-1]
}

func (c *mergeIterator) buildNextBatch(size int) chunkenc.ValueType {
	// All we need to do is get enough batches that our first batch's last entry
	// is before all iterators next entry.
	for len(c.h) > 0 && (len(c.batches) == 0 || c.nextBatchEndTime() >= c.h[0].AtTime()) {
		c.batchesBuf = mergeStreams(c.batches, c.h[0].Batch(), c.batchesBuf, size, &c.hPool, &c.fhPool)
		c.batches = append(c.batches[:0], c.batchesBuf...)

		if c.h[0].Next(size) != chunkenc.ValNone {
			heap.Fix(&c.h, 0)
		} else {
			heap.Pop(&c.h)
		}
	}

	if len(c.batches) > 0 {
		return c.batches[0].ValueType
	}
	return chunkenc.ValNone
}

func (c *mergeIterator) AtTime() int64 {
	return c.batches[0].Timestamps[0]
}

func (c *mergeIterator) Batch() *chunk.Batch {
	return &c.batches[0]
}

func (c *mergeIterator) Err() error {
	return c.currErr
}

type iteratorHeap []iterator

func (h *iteratorHeap) Len() int      { return len(*h) }
func (h *iteratorHeap) Swap(i, j int) { (*h)[i], (*h)[j] = (*h)[j], (*h)[i] }

func (h *iteratorHeap) Less(i, j int) bool {
	iT := (*h)[i].AtTime()
	jT := (*h)[j].AtTime()
	return iT < jT
}

func (h *iteratorHeap) Push(x interface{}) {
	*h = append(*h, x.(iterator))
}

func (h *iteratorHeap) Pop() interface{} {
	old := *h
	n := len(old)
	x := old[n-1]
	*h = old[0 : n-1]
	return x
}

// Build a list of lists of non-overlapping chunks.
func partitionChunks(cs []GenericChunk) [][]GenericChunk {
	sort.Sort(byMinTime(cs))

	css := [][]GenericChunk{}
outer:
	for _, c := range cs {
		for i, cs := range css {
			if cs[len(cs)-1].MaxTime < c.MinTime {
				css[i] = append(css[i], c)
				continue outer
			}
		}
		cs := make([]GenericChunk, 0, len(cs)/(len(css)+1))
		cs = append(cs, c)
		css = append(css, cs)
	}

	return css
}

type byMinTime []GenericChunk

func (b byMinTime) Len() int           { return len(b) }
func (b byMinTime) Swap(i, j int)      { b[i], b[j] = b[j], b[i] }
func (b byMinTime) Less(i, j int) bool { return b[i].MinTime < b[j].MinTime }
